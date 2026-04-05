// octi-timer: Converts schedule.json cron entries into dispatcher timer events.
//
// Replaces 139 crontab entries with a single Go process that fires timer events
// through the Octi Pulpo dispatcher.
//
// Usage:
//
//	OCTI_SCHEDULE=/path/to/schedule.json OCTI_DISPATCHER_URL=http://localhost:8787 octi-timer
//
// For each enabled agent in schedule.json, parses the cron expression and fires
// POST /dispatch/timer at the correct time. The dispatcher handles all intelligence
// (cooldown, claims, budget checks, driver routing).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/cron"
)

// scheduleFile is the parsed schedule.json
type scheduleFile struct {
	MaxWorkers           int                     `json:"max_workers"`
	DefaultTimeoutSeconds int                    `json:"default_timeout_seconds"`
	Agents               map[string]agentConfig  `json:"agents"`
}

type agentConfig struct {
	Driver  string `json:"driver"`
	Cron    string `json:"cron"`
	Repo    string `json:"repo"`
	Box     string `json:"box"`
	Squad   string `json:"squad"`
	Enabled bool   `json:"enabled"`
	Timeout int    `json:"timeout"`
	Model   string `json:"model"`
}

type timerEntry struct {
	Name     string
	Schedule *cron.Schedule
	Config   agentConfig
}

func main() {
	schedPath := envOr("OCTI_SCHEDULE", filepath.Join(os.Getenv("HOME"), "agentguard-workspace", "server", "schedule.json"))
	dispatcherURL := envOr("OCTI_DISPATCHER_URL", "http://localhost:8787")

	// Parse schedule.json
	data, err := os.ReadFile(schedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read schedule: %v\n", err)
		os.Exit(1)
	}

	var sched scheduleFile
	if err := json.Unmarshal(data, &sched); err != nil {
		fmt.Fprintf(os.Stderr, "parse schedule: %v\n", err)
		os.Exit(1)
	}

	// Parse cron expressions for all enabled agents
	var entries []timerEntry
	var parseErrors int
	for name, cfg := range sched.Agents {
		if !cfg.Enabled {
			fmt.Fprintf(os.Stderr, "timer: skipping disabled agent %s\n", name)
			continue
		}
		if cfg.Cron == "" {
			fmt.Fprintf(os.Stderr, "timer: skipping agent %s (no cron expression)\n", name)
			continue
		}

		s, err := cron.Parse(cfg.Cron)
		if err != nil {
			fmt.Fprintf(os.Stderr, "timer: parse error for %s (%s): %v\n", name, cfg.Cron, err)
			parseErrors++
			continue
		}

		entries = append(entries, timerEntry{
			Name:     name,
			Schedule: s,
			Config:   cfg,
		})
	}

	if parseErrors > 0 {
		fmt.Fprintf(os.Stderr, "timer: WARNING: %d agents had cron parse errors\n", parseErrors)
	}

	// Sort by name for deterministic logging
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	fmt.Fprintf(os.Stderr, "octi-timer: loaded %d timer entries from %s\n", len(entries), schedPath)
	fmt.Fprintf(os.Stderr, "octi-timer: dispatcher at %s\n", dispatcherURL)

	// Log next fire times
	now := time.Now()
	for _, e := range entries {
		next := e.Schedule.NextAfter(now)
		fmt.Fprintf(os.Stderr, "  %s: next fire %s (%s)\n", e.Name, next.Format("15:04"), e.Schedule.Raw)
	}

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		fmt.Fprintf(os.Stderr, "octi-timer: received %s, shutting down...\n", sig)
		cancel()
	}()

	// Run the timer loop
	runTimerLoop(ctx, entries, dispatcherURL)
	fmt.Fprintf(os.Stderr, "octi-timer: stopped\n")
}

// runTimerLoop uses a minute-aligned tick to check all schedules.
// This is simpler and more efficient than per-agent goroutines with NextAfter sleeps
// because schedule.json has 139 agents — waking once per minute and checking all of them
// is cheaper than 139 goroutines each sleeping to their next fire time.
func runTimerLoop(ctx context.Context, entries []timerEntry, dispatcherURL string) {
	// Align to next minute boundary
	now := time.Now()
	nextMinute := now.Truncate(time.Minute).Add(time.Minute)
	sleepDur := time.Until(nextMinute)
	fmt.Fprintf(os.Stderr, "octi-timer: waiting %.0fs for next minute boundary\n", sleepDur.Seconds())

	select {
	case <-ctx.Done():
		return
	case <-time.After(sleepDur):
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// Fire immediately at the first minute boundary
	checkAndFire(ctx, entries, dispatcherURL, time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			checkAndFire(ctx, entries, dispatcherURL, t)
		}
	}
}

func checkAndFire(ctx context.Context, entries []timerEntry, dispatcherURL string, t time.Time) {
	var wg sync.WaitGroup
	for _, e := range entries {
		if e.Schedule.Matches(t) {
			wg.Add(1)
			go func(entry timerEntry) {
				defer wg.Done()
				fireTimer(ctx, dispatcherURL, entry, t)
			}(e)
		}
	}
	wg.Wait()
}

func fireTimer(ctx context.Context, dispatcherURL string, entry timerEntry, t time.Time) {
	body, _ := json.Marshal(map[string]interface{}{
		"agent":    entry.Name,
		"priority": 2, // normal priority for timer events
	})

	url := dispatcherURL + "/dispatch/timer"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "timer[%s]: create request: %v\n", entry.Name, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timer[%s]: POST failed: %v\n", entry.Name, err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		fmt.Fprintf(os.Stderr, "timer[%s]: fired at %s -> %s\n", entry.Name, t.Format("15:04"), string(respBody))
	} else {
		fmt.Fprintf(os.Stderr, "timer[%s]: HTTP %d: %s\n", entry.Name, resp.StatusCode, string(respBody))
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
