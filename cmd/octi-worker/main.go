// octi-worker: Redis-native worker that dequeues from Octi Pulpo's priority queue
// and executes agents via run-agent.sh.
//
// Replaces the 32 bash workers polling queue.txt with direct Redis consumption.
//
// Usage:
//
//	OCTI_REDIS_URL=redis://localhost:6379 OCTI_WORKERS=32 octi-worker
//
// Each worker goroutine:
//  1. Calls Dispatcher.Dequeue() to get highest-priority agent
//  2. Spawns run-agent.sh <agent-name> as subprocess
//  3. Waits for completion, records result
//  4. Releases the coordination claim
//  5. Loops
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/coordination"
	"github.com/AgentGuardHQ/octi-pulpo/internal/dispatch"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/redis/go-redis/v9"
)

func main() {
	redisURL := envOr("OCTI_REDIS_URL", "redis://localhost:6379")
	namespace := envOr("OCTI_NAMESPACE", "octi")
	workerCount := envInt("OCTI_WORKERS", 32)
	workspaceDir := envOr("WORKSPACE_DIR", filepath.Join(os.Getenv("HOME"), "agentguard-workspace"))
	runAgentScript := envOr("OCTI_RUN_AGENT", filepath.Join(workspaceDir, "server", "run-agent.sh"))
	scheduleFile := envOr("OCTI_SCHEDULE", filepath.Join(workspaceDir, "server", "schedule.json"))
	pollInterval := 5 * time.Second

	// Validate run-agent.sh exists
	if _, err := os.Stat(runAgentScript); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "run-agent.sh not found at %s\n", runAgentScript)
		os.Exit(1)
	}

	// Set up Redis + dispatcher (we only use Dequeue + ReleaseClaim + RecordWorkerResult)
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse redis url: %v\n", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	// Verify Redis is reachable
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintf(os.Stderr, "redis ping failed: %v\n", err)
		os.Exit(1)
	}

	coord, err := coordination.New(redisURL, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coordination engine: %v\n", err)
		os.Exit(1)
	}
	defer coord.Close()

	healthDir := os.Getenv("AGENTGUARD_HEALTH_DIR")
	router := routing.NewRouter(healthDir)
	eventRouter := dispatch.NewEventRouter(dispatch.DefaultRules())
	dispatcher := dispatch.NewDispatcher(rdb, router, coord, eventRouter, "", namespace)

	// Set up adaptive cooldown profiles
	profiles := dispatch.NewProfileStore(rdb, namespace, eventRouter.CooldownFor)
	dispatcher.SetProfiles(profiles)

	// Load completion chains for reactive dispatch
	chains := dispatch.DefaultChains()
	fmt.Fprintf(os.Stderr, "octi-worker: loaded %d completion chains\n", len(chains))

	fmt.Fprintf(os.Stderr, "octi-worker: starting %d workers, redis %s, namespace %s\n", workerCount, redisURL, namespace)
	fmt.Fprintf(os.Stderr, "octi-worker: run-agent.sh at %s\n", runAgentScript)

	// Graceful shutdown
	shutdownCtx, cancel := context.WithCancel(ctx)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		fmt.Fprintf(os.Stderr, "octi-worker: received %s, shutting down (finishing current agents)...\n", sig)
		cancel()
	}()

	// Launch worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			healthDir := os.Getenv("AGENTGUARD_HEALTH_DIR")
			if healthDir == "" {
				home, _ := os.UserHomeDir()
				healthDir = filepath.Join(home, ".agentguard", "driver-health")
			}
			workerLoop(shutdownCtx, dispatcher, rdb, namespace, runAgentScript, scheduleFile, healthDir, workerID, pollInterval, workspaceDir, chains)
		}(i)
	}

	wg.Wait()
	fmt.Fprintf(os.Stderr, "octi-worker: all workers stopped\n")
}

func workerLoop(
	ctx context.Context,
	d *dispatch.Dispatcher,
	rdb *redis.Client,
	namespace string,
	script string,
	scheduleFile string,
	healthDir string,
	id int,
	pollInterval time.Duration,
	workspaceDir string,
	chains dispatch.ChainConfig,
) {
	for {
		// Check for shutdown
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Dequeue highest-priority agent
		agent, err := d.Dequeue(ctx)
		if err != nil {
			// Context cancelled during dequeue is expected on shutdown
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "worker[%d]: dequeue error: %v\n", id, err)
			sleep(ctx, pollInterval)
			continue
		}

		if agent == "" {
			// Queue empty — sleep and retry
			sleep(ctx, pollInterval)
			continue
		}

		fmt.Fprintf(os.Stderr, "worker[%d]: executing %s\n", id, agent)
		start := time.Now()

		exitCode, captured := executeAgentWithCapture(ctx, script, agent)
		duration := time.Since(start).Seconds()

		releaseCtx := context.Background() // don't use shutdown ctx for cleanup

		if exitCode == 0 {
			fmt.Fprintf(os.Stderr, "worker[%d]: %s completed (%.1fs)\n", id, agent, duration)
		} else {
			fmt.Fprintf(os.Stderr, "worker[%d]: %s failed exit=%d (%.1fs)\n", id, agent, exitCode, duration)
		}

		// Update driver health based on run outcome.
		driver := agentDriver(scheduleFile, agent)
		updateDriverHealth(releaseCtx, rdb, namespace, healthDir, driver, exitCode, captured, id)

		// Release the coordination claim so the agent can be dispatched again
		if err := d.ReleaseClaim(releaseCtx, agent); err != nil {
			fmt.Fprintf(os.Stderr, "worker[%d]: release claim error for %s: %v\n", id, agent, err)
		}

		// Check for commits before recording result (needed for adaptive cooldowns)
		madeCommits := dispatch.CheckForCommits(agent, workspaceDir)

		// Record result for observability + adaptive cooldowns
		d.RecordWorkerResult(releaseCtx, agent, exitCode, duration, madeCommits)

		// Trigger completion chains — dispatch follow-up agents based on result
		if madeCommits {
			fmt.Fprintf(os.Stderr, "worker[%d]: %s made commits, checking chains\n", id, agent)
		}
		chainResults := dispatch.TriggerChains(releaseCtx, d, chains, agent, exitCode, madeCommits)
		for _, cr := range chainResults {
			fmt.Fprintf(os.Stderr, "worker[%d]: chain %s -> %s (%s: %s)\n", id, agent, cr.Agent, cr.Action, cr.Reason)
		}
	}
}

// updateDriverHealth updates on-disk circuit state and Redis budget state after a run.
func updateDriverHealth(ctx context.Context, rdb *redis.Client, namespace, healthDir, driver string, exitCode int, captured string, workerID int) {
	budgetKey := namespace + ":driver-budget:" + driver

	if exitCode != 0 && isCreditExhaustion(captured) {
		fmt.Fprintf(os.Stderr, "worker[%d]: credit exhaustion detected for driver %s — opening circuit\n", workerID, driver)
		if err := routing.MarkDriverOpen(healthDir, driver); err != nil {
			fmt.Fprintf(os.Stderr, "worker[%d]: mark driver open: %v\n", workerID, err)
		}
		pct := 0
		rdb.HSet(ctx, budgetKey,
			"pct", pct,
			"reason", "credit_exhaustion",
			"updated_at", time.Now().UTC().Format(time.RFC3339),
		)
		rdb.Expire(ctx, budgetKey, 4*time.Hour)
		return
	}

	if exitCode == 0 {
		if err := routing.MarkDriverSuccess(healthDir, driver); err != nil {
			fmt.Fprintf(os.Stderr, "worker[%d]: mark driver success: %v\n", workerID, err)
		}
		pct := 80
		rdb.HSet(ctx, budgetKey,
			"pct", pct,
			"reason", "last_run_ok",
			"updated_at", time.Now().UTC().Format(time.RFC3339),
		)
		rdb.Expire(ctx, budgetKey, 4*time.Hour)
	}
}

// executeAgentWithCapture runs the agent script and returns the exit code plus
// the last 16KB of stderr output. Stderr is also forwarded to os.Stderr so
// the caller's log stream is unaffected.
func executeAgentWithCapture(ctx context.Context, script, agent string) (int, string) {
	var buf cappedBuffer
	buf.maxSize = 16 * 1024

	cmd := exec.CommandContext(ctx, "bash", script, agent)
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), buf.String()
		}
		if ctx.Err() != nil {
			return -1, ""
		}
		return 1, buf.String()
	}
	return 0, ""
}

// cappedBuffer is a bytes.Buffer that stops accepting writes after maxSize bytes,
// preventing unbounded memory growth from verbose agent output.
type cappedBuffer struct {
	buf     bytes.Buffer
	maxSize int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len() < c.maxSize {
		rem := c.maxSize - c.buf.Len()
		write := p
		if len(write) > rem {
			write = write[:rem]
		}
		c.buf.Write(write) //nolint:errcheck // bytes.Buffer.Write never errors
	}
	// Always return len(p) so io.MultiWriter does not propagate short-write errors.
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	return c.buf.String()
}

// isCreditExhaustion reports whether the captured output contains evidence of
// a driver's credit or quota being exhausted. Patterns mirror those in
// run-agent.sh and driver-health.sh so the Go worker and bash scripts agree.
func isCreditExhaustion(output string) bool {
	lower := strings.ToLower(output)
	patterns := []string{
		"credit balance",
		"usage limit",
		"quota_exhausted",
		"exhausted your capacity",
		"purchase more credits",
		"budget_exhausted",
		"all drivers at budget cap",
		"no healthy driver available",
		"exhausted your monthly",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// agentDriver looks up the driver for an agent in schedule.json.
// Falls back to "claude-code" when the file is missing or the agent is not listed.
func agentDriver(scheduleFile, agentName string) string {
	data, err := os.ReadFile(scheduleFile)
	if err != nil {
		return "claude-code"
	}
	var sched struct {
		Agents map[string]struct {
			Driver string `json:"driver"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &sched); err != nil {
		return "claude-code"
	}
	if a, ok := sched.Agents[agentName]; ok && a.Driver != "" {
		return a.Driver
	}
	return "claude-code"
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
