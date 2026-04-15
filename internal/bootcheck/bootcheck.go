// Package bootcheck runs a startup self-audit of telemetry wiring.
package bootcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/redis/go-redis/v9"
)

type Status string

const (
	StatusGreen  Status = "green"
	StatusYellow Status = "yellow"
	StatusRed    Status = "red"
)

type Result struct {
	Name    string    `json:"name"`
	Status  Status    `json:"status"`
	Message string    `json:"message"`
	RanAt   time.Time `json:"ran_at"`
}

type CheckReport struct {
	StartedAt   time.Time `json:"started_at"`
	Results     []Result  `json:"results"`
	RedCount    int       `json:"red_count"`
	YellowCount int       `json:"yellow_count"`
	GreenCount  int       `json:"green_count"`
}

type Deps struct {
	RDB         *redis.Client
	Namespace   string
	Router      *routing.Router
	Benchmark   *dispatch.BenchmarkTracker
	Profiles    *dispatch.ProfileStore
	GitHubToken string
	HTTPGet     func(ctx context.Context, url, token string) (int, error)
	Now         func() time.Time
}

func Run(ctx context.Context, d Deps) *CheckReport {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	rep := &CheckReport{StartedAt: now()}
	checks := []struct {
		name string
		fn   func(context.Context) Result
	}{
		{"dispatch_log_writable", func(c context.Context) Result { return checkDispatchLog(c, d, now) }},
		{"benchmark_counters_wired", func(c context.Context) Result { return checkBenchmarkCounters(c, d) }},
		{"health_report_fresh", func(c context.Context) Result { return checkHealthFresh(c, d, now) }},
		{"leaderboard_sink_wired", func(c context.Context) Result { return checkLeaderboardSink(c, d) }},
		{"adapter_reachability", func(c context.Context) Result { return checkAdapterReachability(c, d) }},
	}
	for _, ch := range checks {
		subCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		res := ch.fn(subCtx)
		cancel()
		res.Name = ch.name
		res.RanAt = now()
		rep.Results = append(rep.Results, res)
		switch res.Status {
		case StatusRed:
			rep.RedCount++
		case StatusYellow:
			rep.YellowCount++
		case StatusGreen:
			rep.GreenCount++
		}
	}
	return rep
}

func checkDispatchLog(ctx context.Context, d Deps, now func() time.Time) Result {
	if d.RDB == nil {
		return Result{Status: StatusYellow, Message: "redis client not wired; skipped"}
	}
	key := d.Namespace + ":dispatch-log"
	canaryID := fmt.Sprintf("bootcheck-canary-%d", now().UnixNano())
	payload := map[string]any{"agent": canaryID, "result": "bootcheck", "timestamp": now().UTC().Format(time.RFC3339Nano)}
	data, _ := json.Marshal(payload)
	if err := d.RDB.LPush(ctx, key, data).Err(); err != nil {
		return Result{Status: StatusRed, Message: "LPUSH failed: " + err.Error()}
	}
	raw, err := d.RDB.LRange(ctx, key, 0, 4).Result()
	if err != nil {
		return Result{Status: StatusRed, Message: "LRANGE failed: " + err.Error()}
	}
	found := false
	for _, r := range raw {
		if strings.Contains(r, canaryID) {
			found = true
			break
		}
	}
	_ = d.RDB.LRem(ctx, key, 1, data).Err()
	if !found {
		return Result{Status: StatusRed, Message: "canary write not visible on read-back (sink unwired)"}
	}
	return Result{Status: StatusGreen, Message: "round-trip OK"}
}

func checkBenchmarkCounters(ctx context.Context, d Deps) Result {
	if d.Benchmark == nil || d.RDB == nil {
		return Result{Status: StatusYellow, Message: "benchmark tracker not wired; skipped"}
	}
	workerLen, _ := d.RDB.LLen(ctx, d.Namespace+":worker-results").Result()
	dispatchLen, _ := d.RDB.LLen(ctx, d.Namespace+":dispatch-log").Result()
	hasActivity := workerLen > 0 || dispatchLen > 0
	m, err := d.Benchmark.Compute(ctx)
	if err != nil {
		return Result{Status: StatusRed, Message: "Compute failed: " + err.Error()}
	}
	if !hasActivity {
		return Result{Status: StatusGreen, Message: "idle (no worker-results or dispatch-log entries)"}
	}
	anyNonZero := m.ActiveAgents > 0 || m.PRsPerHour > 0 || m.CommitsPerRun > 0 || m.QueueDepth > 0 || m.PassRate > 0 || m.QAIX > 0
	if !anyNonZero {
		return Result{Status: StatusRed, Message: fmt.Sprintf("activity present (worker=%d, dispatch=%d) but all metrics zero — counters unwired", workerLen, dispatchLen)}
	}
	return Result{Status: StatusGreen, Message: fmt.Sprintf("active_agents=%d, qaix=%.1f", m.ActiveAgents, m.QAIX)}
}

func checkHealthFresh(ctx context.Context, d Deps, now func() time.Time) Result {
	if d.Router == nil {
		return Result{Status: StatusYellow, Message: "router not wired; skipped"}
	}
	_ = ctx
	report := d.Router.HealthReport()
	if len(report) == 0 {
		return Result{Status: StatusYellow, Message: "no drivers discovered"}
	}
	var stale []string
	for _, h := range report {
		if h.Name == "" {
			// Defensive: a corrupt/legacy health file produced an entry with no
			// driver name. Log and skip so it doesn't surface as a ghost in the
			// bootcheck output (see workspace#408). Source file should be
			// cleaned up by hopper's orphan-cleanup pass.
			log.Printf("bootcheck: skipping health entry with empty driver name in %s (corrupt file; flag for orphan-cleanup)", d.Router.HealthDir())
			continue
		}
		if h.CircuitState != "CLOSED" {
			continue
		}
		if h.LastSuccess == "" {
			stale = append(stale, h.Name+" (never-succeeded)")
			continue
		}
		t, err := time.Parse(time.RFC3339, h.LastSuccess)
		if err != nil {
			continue
		}
		if now().Sub(t) > 30*24*time.Hour {
			stale = append(stale, fmt.Sprintf("%s (last_success=%s)", h.Name, h.LastSuccess))
		}
	}
	if len(stale) > 0 {
		return Result{Status: StatusRed, Message: "stale CLOSED drivers: " + strings.Join(stale, ", ")}
	}
	return Result{Status: StatusGreen, Message: fmt.Sprintf("%d drivers fresh", len(report))}
}

func checkLeaderboardSink(ctx context.Context, d Deps) Result {
	if d.Profiles == nil || d.RDB == nil {
		return Result{Status: StatusYellow, Message: "profile store not wired; skipped"}
	}
	dispatchLen, _ := d.RDB.LLen(ctx, d.Namespace+":dispatch-log").Result()
	entries, err := d.Profiles.Leaderboard(ctx)
	if err != nil {
		return Result{Status: StatusRed, Message: "leaderboard read failed: " + err.Error()}
	}
	if dispatchLen == 0 {
		return Result{Status: StatusGreen, Message: "no dispatch activity (vacuous)"}
	}
	if len(entries) == 0 {
		return Result{Status: StatusRed, Message: fmt.Sprintf("dispatch-log has %d entries but leaderboard is empty — sink unwired", dispatchLen)}
	}
	return Result{Status: StatusGreen, Message: fmt.Sprintf("dispatch=%d, leaderboard=%d agents", dispatchLen, len(entries))}
}

func checkAdapterReachability(ctx context.Context, d Deps) Result {
	if d.GitHubToken == "" {
		return Result{Status: StatusYellow, Message: "no GH token configured; skipped"}
	}
	getFn := d.HTTPGet
	if getFn == nil {
		getFn = defaultHTTPGet
	}
	status, err := getFn(ctx, "https://api.github.com/", d.GitHubToken)
	if err != nil {
		return Result{Status: StatusYellow, Message: "gh probe error: " + err.Error()}
	}
	if status < 200 || status >= 400 {
		return Result{Status: StatusYellow, Message: fmt.Sprintf("gh probe returned HTTP %d", status)}
	}
	return Result{Status: StatusGreen, Message: fmt.Sprintf("gh probe HTTP %d", status)}
}

func (r *CheckReport) Render(w io.Writer) {
	fmt.Fprintf(w, "octi-pulpo bootcheck @ %s  [%d green / %d yellow / %d red]\n", r.StartedAt.Format(time.RFC3339), r.GreenCount, r.YellowCount, r.RedCount)
	for _, res := range r.Results {
		tag := "OK   "
		switch res.Status {
		case StatusRed:
			tag = "RED  "
		case StatusYellow:
			tag = "WARN "
		}
		fmt.Fprintf(w, "  [%s] %-28s %s\n", tag, res.Name, res.Message)
	}
	if r.RedCount > 0 {
		fmt.Fprintf(w, "  WARNING: %d bootcheck failures — telemetry may be lying. See `bootcheck_status` MCP tool.\n", r.RedCount)
	}
}

type Cache struct {
	mu   sync.RWMutex
	last *CheckReport
}

func NewCache() *Cache { return &Cache{} }

func (c *Cache) Set(r *CheckReport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = r
}

func (c *Cache) Get() *CheckReport {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.last
}
