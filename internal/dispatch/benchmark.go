package dispatch

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// Metrics captures swarm throughput and efficiency data.
type Metrics struct {
	PRsPerHour       float64 `json:"prs_per_hour"`
	CommitsPerRun    float64 `json:"commits_per_run"`
	WastePercent     float64 `json:"waste_percent"`      // % of runs <10s with 0 commits
	BudgetEfficiency float64 `json:"budget_efficiency"`   // commits per dollar (estimate)
	ActiveAgents     int     `json:"active_agents"`
	QueueDepth       int64   `json:"queue_depth"`
	PassRate         float64 `json:"pass_rate"`
}

// BenchmarkTracker computes throughput metrics from worker results stored in Redis.
type BenchmarkTracker struct {
	rdb       *redis.Client
	namespace string
}

// NewBenchmarkTracker creates a benchmark tracker.
func NewBenchmarkTracker(rdb *redis.Client, namespace string) *BenchmarkTracker {
	return &BenchmarkTracker{rdb: rdb, namespace: namespace}
}

// workerResult is the structure stored by RecordWorkerResult.
type workerResult struct {
	Agent       string  `json:"agent"`
	ExitCode    int     `json:"exit_code"`
	DurationSec float64 `json:"duration_sec"`
	Timestamp   string  `json:"timestamp"`
}

// Compute calculates current swarm metrics from the last N worker results.
func (bt *BenchmarkTracker) Compute(ctx context.Context) (Metrics, error) {
	var m Metrics

	// Read last 100 worker results
	raw, err := bt.rdb.LRange(ctx, bt.key("worker-results"), 0, 99).Result()
	if err != nil {
		return m, err
	}

	if len(raw) == 0 {
		return m, nil
	}

	var results []workerResult
	for _, r := range raw {
		var wr workerResult
		if err := json.Unmarshal([]byte(r), &wr); err != nil {
			continue
		}
		results = append(results, wr)
	}

	if len(results) == 0 {
		return m, nil
	}

	// Compute pass rate and waste
	var passes, waste int
	var totalDuration float64

	for _, r := range results {
		if r.ExitCode == 0 {
			passes++
		}
		if r.DurationSec < 10 {
			waste++
		}
		totalDuration += r.DurationSec
	}

	n := float64(len(results))
	m.PassRate = float64(passes) / n
	m.WastePercent = float64(waste) / n * 100

	// Estimate commits per run from dispatch log (PRs opened are approximated
	// by counting dispatches to pr-merger agents or review agents)
	dispatchRaw, _ := bt.rdb.LRange(ctx, bt.key("dispatch-log"), 0, 99).Result()
	var prDispatches int
	for _, d := range dispatchRaw {
		var rec DispatchRecord
		if err := json.Unmarshal([]byte(d), &rec); err != nil {
			continue
		}
		if rec.Result == "dispatched" {
			// Count dispatches as proxy for commits
			m.CommitsPerRun += 0.1 // rough estimate
		}
		if containsAny(rec.Agent, "pr-merger", "pr-review", "reviewer") {
			prDispatches++
		}
	}

	if n > 0 {
		m.CommitsPerRun = m.CommitsPerRun / n * 10 // normalize
	}

	// PRs per hour: estimate from time window of results
	if len(results) >= 2 {
		oldest, _ := time.Parse(time.RFC3339, results[len(results)-1].Timestamp)
		newest, _ := time.Parse(time.RFC3339, results[0].Timestamp)
		hours := newest.Sub(oldest).Hours()
		if hours > 0 {
			m.PRsPerHour = float64(prDispatches) / hours
		}
	}

	// Budget efficiency: rough estimate (assume $0.01 per 60s of compute)
	if totalDuration > 0 {
		costEstimate := totalDuration / 60.0 * 0.01
		if costEstimate > 0 {
			m.BudgetEfficiency = float64(passes) / costEstimate
		}
	}

	// Queue depth
	m.QueueDepth, _ = bt.rdb.ZCard(ctx, bt.key("dispatch-queue")).Result()

	// Active agents: count unique agents in recent results
	agentSet := make(map[string]bool)
	for _, r := range results {
		agentSet[r.Agent] = true
	}
	m.ActiveAgents = len(agentSet)

	return m, nil
}

func (bt *BenchmarkTracker) key(suffix string) string {
	return bt.namespace + ":" + suffix
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
