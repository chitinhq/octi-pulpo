package dispatch

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// AgentStats is the per-agent productivity counter record stored in Redis as a hash.
//
// Redis key schema: {namespace}:agent_stats:{agent}
// Fields:
//
//	dispatches_total   — counter, incremented on every successful Dispatch
//	successes_total    — counter, incremented on completion with exit_code=0 && had_commits
//	last_pr_merged_at  — RFC3339 timestamp of last successful completion
//	last_pr_url        — URL of the last merged PR (optional, may be empty)
//
// This is the sink that powers agent_leaderboard. Prior to this, RecordRun was the
// only writer and only fired from octi-worker — meaning leaderboard was empty in
// production where dispatches go to GitHub Actions / Anthropic API.
type AgentStats struct {
	Agent           string `json:"agent"`
	DispatchesTotal int64  `json:"dispatches_total"`
	SuccessesTotal  int64  `json:"successes_total"`
	LastPRMergedAt  string `json:"last_pr_merged_at,omitempty"`
	LastPRURL       string `json:"last_pr_url,omitempty"`
}

// SuccessRate returns successes_total / dispatches_total, or 0 if no dispatches.
func (s AgentStats) SuccessRate() float64 {
	if s.DispatchesTotal == 0 {
		return 0
	}
	return float64(s.SuccessesTotal) / float64(s.DispatchesTotal)
}

func (ps *ProfileStore) statsKey(agent string) string {
	return ps.namespace + ":agent_stats:" + agent
}

// RecordDispatch increments the dispatches_total counter for an agent.
// Called from Dispatcher.Dispatch after a successful dispatch (action="dispatched").
func (ps *ProfileStore) RecordDispatch(ctx context.Context, agent string) error {
	if ps == nil || ps.rdb == nil || agent == "" {
		return nil
	}
	return ps.rdb.HIncrBy(ctx, ps.statsKey(agent), "dispatches_total", 1).Err()
}

// RecordSuccess increments the successes_total counter and records the merge timestamp + PR URL.
// Called from Dispatcher.RecordWorkerResult on a successful run with commits.
// prURL may be empty (we don't always know the PR at completion time).
func (ps *ProfileStore) RecordSuccess(ctx context.Context, agent string, prURL string) error {
	if ps == nil || ps.rdb == nil || agent == "" {
		return nil
	}
	pipe := ps.rdb.Pipeline()
	pipe.HIncrBy(ctx, ps.statsKey(agent), "successes_total", 1)
	pipe.HSet(ctx, ps.statsKey(agent), "last_pr_merged_at", time.Now().UTC().Format(time.RFC3339))
	if prURL != "" {
		pipe.HSet(ctx, ps.statsKey(agent), "last_pr_url", prURL)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// GetStats reads the agent_stats hash for one agent.
func (ps *ProfileStore) GetStats(ctx context.Context, agent string) (AgentStats, error) {
	stats := AgentStats{Agent: agent}
	if ps == nil || ps.rdb == nil {
		return stats, nil
	}
	m, err := ps.rdb.HGetAll(ctx, ps.statsKey(agent)).Result()
	if err != nil {
		return stats, fmt.Errorf("hgetall agent_stats %s: %w", agent, err)
	}
	if v, ok := m["dispatches_total"]; ok {
		stats.DispatchesTotal, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := m["successes_total"]; ok {
		stats.SuccessesTotal, _ = strconv.ParseInt(v, 10, 64)
	}
	stats.LastPRMergedAt = m["last_pr_merged_at"]
	stats.LastPRURL = m["last_pr_url"]
	return stats, nil
}

// AllStats scans every agent_stats hash in this namespace.
func (ps *ProfileStore) AllStats(ctx context.Context) (map[string]AgentStats, error) {
	out := map[string]AgentStats{}
	if ps == nil || ps.rdb == nil {
		return out, nil
	}
	pattern := ps.namespace + ":agent_stats:*"
	prefix := ps.namespace + ":agent_stats:"

	iter := ps.rdb.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		agent := key[len(prefix):]
		stats, err := ps.GetStats(ctx, agent)
		if err != nil {
			continue
		}
		out[agent] = stats
	}
	if err := iter.Err(); err != nil {
		return out, fmt.Errorf("scan agent_stats: %w", err)
	}
	return out, nil
}
