package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// AgentProfile tracks an agent's recent execution history for adaptive cooldown tuning.
type AgentProfile struct {
	Name             string        `json:"name"`
	RecentResults    []RunResult   `json:"recent_results"`   // last 10
	AvgDuration      float64       `json:"avg_duration_s"`
	AvgCommits       float64       `json:"avg_commits"`
	FailRate         float64       `json:"fail_rate"`
	CurrentCooldown  time.Duration `json:"current_cooldown"`
	ConsecutiveIdles int           `json:"consecutive_idles"`
	ConsecutiveFails int           `json:"consecutive_fails"`
	// TriageFlag is set when the agent accumulates 3+ consecutive failures and
	// needs human review before being dispatched aggressively again.
	TriageFlag bool `json:"triage_flag,omitempty"`
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
	rdb            *redis.Client
	namespace      string
	staticCooldown func(agent string) time.Duration
	// budgetHealthFn returns the fraction of drivers that are healthy (0.0–1.0).
	// 1.0 = all drivers CLOSED; 0.0 = all drivers OPEN (budget exhausted).
	// Optional — if nil, budget signal is ignored.
	budgetHealthFn func() float64
}

// NewProfileStore creates a profile store.
func NewProfileStore(rdb *redis.Client, namespace string, staticCooldown func(string) time.Duration) *ProfileStore {
	return &ProfileStore{
		rdb:            rdb,
		namespace:      namespace,
		staticCooldown: staticCooldown,
	}
}

// SetBudgetHealthFn wires a live driver-health signal into adaptive cooldown.
// fn should return the fraction of drivers with CLOSED circuits (0.0–1.0).
// The dispatcher in main.go provides this via router.HealthReport().
func (ps *ProfileStore) SetBudgetHealthFn(fn func() float64) {
	ps.budgetHealthFn = fn
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

	// Track consecutive idles (short runs with no output)
	if result.Duration < 10 && !result.HadCommits {
		profile.ConsecutiveIdles++
	} else {
		profile.ConsecutiveIdles = 0
	}

	// Track consecutive failures; set triage flag at threshold
	if result.ExitCode != 0 {
		profile.ConsecutiveFails++
		if profile.ConsecutiveFails >= 3 {
			profile.TriageFlag = true
		}
	} else {
		profile.ConsecutiveFails = 0
		profile.TriageFlag = false
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

// AdaptiveCooldown computes the optimal cooldown for an agent based on recent performance
// and global driver health.
//
// Rules (checked in order):
//  1. Triage: 3+ consecutive failures → 12h (needs human review)
//  2. Productive (commits > 0, duration > 30s) → 5 min, possibly 2.5 min if budget healthy
//  3. Idle (<10s, 0 commits) → double current, max 6h; if budget tight → triple, max 6h
//  4. Failing (>50% fail rate) → double current, max 2h
//  5. Budget tight (<30% healthy drivers) → 3× static cooldown
//  6. Default → static cooldown from event rules
func (ps *ProfileStore) AdaptiveCooldown(ctx context.Context, agent string) time.Duration {
	profile, err := ps.GetProfile(ctx, agent)
	if err != nil || len(profile.RecentResults) == 0 {
		// No history: apply budget multiplier to static cooldown if budget is tight
		return ps.applyBudgetMultiplier(ps.staticCooldown(agent))
	}

	// 1. Triage: agent needs human review — back off hard
	if profile.TriageFlag {
		return 12 * time.Hour
	}

	// Current cooldown baseline (for multiplier calculations)
	current := profile.CurrentCooldown
	if current == 0 {
		current = ps.staticCooldown(agent)
	}
	if current == 0 {
		current = 10 * time.Minute // absolute fallback
	}

	// Look at the most recent result for immediate signals
	latest := profile.RecentResults[len(profile.RecentResults)-1]

	// 2. Productive: commits and meaningful duration
	if latest.HadCommits && latest.Duration > 30 {
		d := 5 * time.Minute
		// Healthy budget (only when health fn configured): reward hot streaks
		if ps.budgetHealthFn != nil && ps.budgetHealth() > 0.8 {
			d = 150 * time.Second // ~2.5 min
		}
		return d
	}

	// 3. Idle: short runs with no output
	if latest.Duration < 10 && !latest.HadCommits {
		multiplier := time.Duration(2)
		if ps.budgetHealthFn != nil && ps.budgetHealth() < 0.3 {
			multiplier = 3 // conserve budget when drivers are stressed
		}
		result := current * multiplier
		if result > 6*time.Hour {
			result = 6 * time.Hour
		}
		return result
	}

	// 4. Failing: high failure rate
	if profile.FailRate > 0.5 {
		doubled := current * 2
		if doubled > 2*time.Hour {
			doubled = 2 * time.Hour
		}
		return doubled
	}

	// 5. Default: static cooldown, adjusted for budget health
	return ps.applyBudgetMultiplier(ps.staticCooldown(agent))
}

// budgetHealth returns the fraction of healthy drivers via budgetHealthFn.
// Returns 1.0 (fully healthy) when no health function is configured.
func (ps *ProfileStore) budgetHealth() float64 {
	if ps.budgetHealthFn == nil {
		return 1.0
	}
	return ps.budgetHealthFn()
}

// applyBudgetMultiplier scales a cooldown based on driver health.
// Budget tight (<30% healthy): 3× cooldown. Normal: unchanged.
// No-ops when budgetHealthFn is not configured.
func (ps *ProfileStore) applyBudgetMultiplier(d time.Duration) time.Duration {
	if ps.budgetHealthFn != nil && ps.budgetHealth() < 0.3 {
		return d * 3
	}
	return d
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

// AllProfiles returns every agent profile stored in this namespace.
// Uses Redis SCAN to enumerate profile keys without blocking.
func (ps *ProfileStore) AllProfiles(ctx context.Context) ([]AgentProfile, error) {
	pattern := ps.namespace + ":profile:*"
	prefix := ps.namespace + ":profile:"

	var keys []string
	iter := ps.rdb.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan profiles: %w", err)
	}
	if len(keys) == 0 {
		return nil, nil
	}

	values, err := ps.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget profiles: %w", err)
	}

	profiles := make([]AgentProfile, 0, len(keys))
	for i, v := range values {
		if v == nil {
			continue
		}
		var p AgentProfile
		if err := json.Unmarshal([]byte(v.(string)), &p); err != nil {
			continue
		}
		if p.Name == "" {
			p.Name = strings.TrimPrefix(keys[i], prefix)
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}
