package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// AgentProfile tracks an agent's recent execution history for adaptive cooldown tuning.
type AgentProfile struct {
	Name             string        `json:"name"`
	RecentResults    []RunResult   `json:"recent_results"`    // last 10
	AvgDuration      float64       `json:"avg_duration_s"`
	AvgCommits       float64       `json:"avg_commits"`
	FailRate         float64       `json:"fail_rate"`
	CurrentCooldown  time.Duration `json:"current_cooldown"`
	ConsecutiveIdles int           `json:"consecutive_idles"`
}

// RunResult is a single agent execution record.
type RunResult struct {
	ExitCode   int     `json:"exit_code"`
	Duration   float64 `json:"duration_s"`
	HadCommits bool    `json:"had_commits"`
	Timestamp  string  `json:"timestamp"`
}

// ProfileStore manages agent execution profiles in Redis.
type ProfileStore struct {
	rdb       *redis.Client
	namespace string
	// staticCooldown provides the fallback cooldown from event rules
	staticCooldown func(agent string) time.Duration
}

// NewProfileStore creates a profile store.
func NewProfileStore(rdb *redis.Client, namespace string, staticCooldown func(string) time.Duration) *ProfileStore {
	return &ProfileStore{
		rdb:            rdb,
		namespace:      namespace,
		staticCooldown: staticCooldown,
	}
}

// RecordRun appends a run result to the agent's profile, keeping the last 10.
func (ps *ProfileStore) RecordRun(ctx context.Context, agent string, result RunResult) error {
	key := ps.profileKey(agent)

	profile, _ := ps.GetProfile(ctx, agent)
	profile.Name = agent
	profile.RecentResults = append(profile.RecentResults, result)

	// Keep last 10
	if len(profile.RecentResults) > 10 {
		profile.RecentResults = profile.RecentResults[len(profile.RecentResults)-10:]
	}

	// Recompute aggregates
	ps.recompute(&profile)

	// Track consecutive idles
	if result.Duration < 10 && !result.HadCommits {
		profile.ConsecutiveIdles++
	} else {
		profile.ConsecutiveIdles = 0
	}

	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	return ps.rdb.Set(ctx, key, data, 0).Err()
}

// GetProfile reads an agent's execution profile from Redis.
func (ps *ProfileStore) GetProfile(ctx context.Context, agent string) (AgentProfile, error) {
	key := ps.profileKey(agent)
	raw, err := ps.rdb.Get(ctx, key).Result()
	if err != nil {
		return AgentProfile{Name: agent}, nil // not found is ok
	}

	var profile AgentProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return AgentProfile{Name: agent}, fmt.Errorf("parse profile for %s: %w", agent, err)
	}
	return profile, nil
}

// AdaptiveCooldown computes the optimal cooldown for an agent based on recent performance.
//
// Rules (checked in order):
//   - Productive (commits > 0, duration > 30s): 5 min
//   - Idle (<10s, 0 commits): double current, max 6h
//   - Failing (>50% fail rate): double current, max 2h
//   - Default: use the static cooldown from event rules
func (ps *ProfileStore) AdaptiveCooldown(ctx context.Context, agent string) time.Duration {
	profile, err := ps.GetProfile(ctx, agent)
	if err != nil || len(profile.RecentResults) == 0 {
		return ps.staticCooldown(agent)
	}

	// Look at the most recent result for immediate signals
	latest := profile.RecentResults[len(profile.RecentResults)-1]

	// Productive: commits and meaningful duration
	if latest.HadCommits && latest.Duration > 30 {
		return 5 * time.Minute
	}

	// Current cooldown baseline
	current := profile.CurrentCooldown
	if current == 0 {
		current = ps.staticCooldown(agent)
	}
	if current == 0 {
		current = 10 * time.Minute // absolute fallback
	}

	// Idle: short runs with no output
	if latest.Duration < 10 && !latest.HadCommits {
		doubled := current * 2
		if doubled > 6*time.Hour {
			doubled = 6 * time.Hour
		}
		return doubled
	}

	// Failing: high failure rate
	if profile.FailRate > 0.5 {
		doubled := current * 2
		if doubled > 2*time.Hour {
			doubled = 2 * time.Hour
		}
		return doubled
	}

	return ps.staticCooldown(agent)
}

// recompute recalculates aggregate metrics from recent results.
func (ps *ProfileStore) recompute(p *AgentProfile) {
	if len(p.RecentResults) == 0 {
		return
	}

	var totalDuration float64
	var commits int
	var failures int

	for _, r := range p.RecentResults {
		totalDuration += r.Duration
		if r.HadCommits {
			commits++
		}
		if r.ExitCode != 0 {
			failures++
		}
	}

	n := float64(len(p.RecentResults))
	p.AvgDuration = totalDuration / n
	p.AvgCommits = float64(commits) / n
	p.FailRate = float64(failures) / n
}

func (ps *ProfileStore) profileKey(agent string) string {
	return ps.namespace + ":profile:" + agent
}
