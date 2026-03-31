// Package admission provides intake scoring, concurrency budgeting, and domain
// locking for the Octi Pulpo swarm coordinator. Together these answer the
// question "should this work run NOW?" before dispatch ever fires.
//
// Three primitives:
//
//  1. Intake scoring — Assess a task and return ACCEPT/DEFER/REJECT/PREFLIGHT.
//  2. Concurrency gates — Enforce max-N active agents per scope (repo/squad/global).
//  3. Domain locks — Exclusive locks on file paths, branches, or services.
package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Verdict is the outcome of a task intake check.
type Verdict string

const (
	VerdictAccept   Verdict = "ACCEPT"
	VerdictDefer    Verdict = "DEFER"
	VerdictReject   Verdict = "REJECT"
	VerdictPreflight Verdict = "ROUTE_TO_PREFLIGHT"
)

// TaskSpec describes a candidate task for admission scoring.
type TaskSpec struct {
	// Title is a short description of the task.
	Title string `json:"title"`
	// Squad is the owning squad (e.g. "kernel", "octi-pulpo").
	Squad string `json:"squad"`
	// Repo is the target repository (e.g. "AgentGuardHQ/agentguard").
	Repo string `json:"repo"`
	// FilePaths lists files this task will touch (used for blast-radius scoring).
	FilePaths []string `json:"file_paths,omitempty"`
	// Branch is the target branch (empty = main).
	Branch string `json:"branch,omitempty"`
	// Priority is 0 (CRITICAL) to 4 (BACKGROUND).
	Priority int `json:"priority"`
	// IsReversible indicates whether the changes can be easily undone.
	IsReversible bool `json:"is_reversible"`
	// SpecClarity is 0.0–1.0: how complete/unambiguous the task spec is.
	SpecClarity float64 `json:"spec_clarity"`
	// EstimatedTokens is the approximate token cost for the run.
	EstimatedTokens int `json:"estimated_tokens,omitempty"`
}

// IntakeScore is the result of scoring a task.
type IntakeScore struct {
	Verdict         Verdict  `json:"verdict"`
	Score           float64  `json:"score"`   // 0.0–1.0 composite
	BlastRadius     int      `json:"blast_radius"`
	Reasons         []string `json:"reasons"`
	SuggestedAction string   `json:"suggested_action"`
}

// DomainLock is an active lock on a contested surface.
type DomainLock struct {
	LockID    string `json:"lock_id"`
	Domain    string `json:"domain"`  // e.g. "file:api/orders/", "branch:feat/auth", "service:payments"
	Holder    string `json:"holder"`  // agent identity
	AcquiredAt string `json:"acquired_at"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// Gate manages admission: concurrency limits + domain locks.
type Gate struct {
	rdb *redis.Client
	ns  string
}

// New creates an admission Gate backed by Redis.
func New(rdb *redis.Client, namespace string) *Gate {
	return &Gate{rdb: rdb, ns: namespace}
}

// ─── Intake Scoring ──────────────────────────────────────────────────────────

// Score evaluates a task and returns a verdict and reasoning.
// Scoring rules (cumulative penalties lower the score below thresholds):
//
//   Base score: 1.0
//   - Blast radius >10 files:  -0.20
//   - Blast radius >20 files:  -0.40 total
//   - Spec clarity <0.5:       -0.30  → routes to PREFLIGHT
//   - Non-reversible + P≥2:    -0.15
//   - EstimatedTokens >50000:  -0.10
//
// Thresholds:
//   score ≥ 0.85 → ACCEPT
//   score ≥ 0.50 → DEFER (or PREFLIGHT when clarity low)
//   score  < 0.50 → REJECT
func (g *Gate) Score(ctx context.Context, task TaskSpec) IntakeScore {
	score := 1.0
	var reasons []string

	blastRadius := len(task.FilePaths)

	// Blast radius penalties
	if blastRadius > 20 {
		score -= 0.40
		reasons = append(reasons, fmt.Sprintf("blast radius %d files (>20): high merge conflict risk", blastRadius))
	} else if blastRadius > 10 {
		score -= 0.20
		reasons = append(reasons, fmt.Sprintf("blast radius %d files (>10): moderate risk", blastRadius))
	}

	// Spec clarity gate — low clarity routes to Preflight
	if task.SpecClarity < 0.5 {
		score -= 0.30
		reasons = append(reasons, fmt.Sprintf("spec clarity %.1f (<0.5): task needs more definition", task.SpecClarity))
	}

	// Irreversible non-critical work is riskier
	if !task.IsReversible && task.Priority >= 2 {
		score -= 0.15
		reasons = append(reasons, "non-reversible change with non-critical priority")
	}

	// Token cost gate
	if task.EstimatedTokens > 50000 {
		score -= 0.10
		reasons = append(reasons, fmt.Sprintf("high token cost estimate (%d tokens)", task.EstimatedTokens))
	}

	// Determine verdict
	var verdict Verdict
	var action string
	switch {
	case task.SpecClarity < 0.5:
		// Low clarity always routes to Preflight regardless of score
		verdict = VerdictPreflight
		action = "Run Preflight to clarify task spec before dispatching"
	case score >= 0.85:
		verdict = VerdictAccept
		action = "Dispatch immediately"
	case score >= 0.50:
		verdict = VerdictDefer
		action = "Queue for off-peak dispatch or split into smaller tasks"
	default:
		verdict = VerdictReject
		action = "Reject — too risky or expensive without further scoping"
	}

	if len(reasons) == 0 {
		reasons = []string{"all checks passed"}
	}

	return IntakeScore{
		Verdict:         verdict,
		Score:           score,
		BlastRadius:     blastRadius,
		Reasons:         reasons,
		SuggestedAction: action,
	}
}

// ─── Concurrency Gates ───────────────────────────────────────────────────────

// acquireSlotScript atomically checks and increments a concurrency counter.
// Returns 1 if slot acquired (counter was below limit), 0 if at limit.
//
// KEYS[1] = counter key
// ARGV[1] = limit (int)
// ARGV[2] = ttl_seconds (int)
var acquireSlotScript = redis.NewScript(`
local count = redis.call('GET', KEYS[1])
count = tonumber(count) or 0
local limit = tonumber(ARGV[1])
if count >= limit then
  return 0
end
local new_count = redis.call('INCR', KEYS[1])
-- Set TTL only on first acquisition to prevent counter from living forever
if new_count == 1 then
  redis.call('EXPIRE', KEYS[1], tonumber(ARGV[2]))
end
return 1
`)

// ConcurrencyScope identifies a scoped concurrency limit.
type ConcurrencyScope struct {
	// Type is "repo", "squad", or "global".
	Type string `json:"type"`
	// Key is the scope identifier (repo name, squad name, or "global").
	Key string `json:"key"`
	// Limit is the maximum number of concurrent agents for this scope.
	Limit int `json:"limit"`
}

// AcquireSlot attempts to acquire a concurrency slot in the given scope.
// Returns true if acquired, false if at the limit.
// Slots auto-expire after ttl to handle agent crashes.
func (g *Gate) AcquireSlot(ctx context.Context, scope ConcurrencyScope, ttl time.Duration) (bool, error) {
	key := g.concurrencyKey(scope)
	result, err := acquireSlotScript.Run(ctx, g.rdb,
		[]string{key},
		scope.Limit,
		int(ttl.Seconds()),
	).Int()
	if err != nil {
		return false, fmt.Errorf("acquire concurrency slot %s/%s: %w", scope.Type, scope.Key, err)
	}
	return result == 1, nil
}

// ReleaseSlot decrements the concurrency counter for a scope.
// Call this when an agent finishes its task.
func (g *Gate) ReleaseSlot(ctx context.Context, scope ConcurrencyScope) error {
	key := g.concurrencyKey(scope)
	result, err := g.rdb.Decr(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("release concurrency slot %s/%s: %w", scope.Type, scope.Key, err)
	}
	// Guard against negative counts from unbalanced releases
	if result < 0 {
		g.rdb.Set(ctx, key, 0, 0)
	}
	return nil
}

// SlotUsage returns the current concurrency count and limit for a scope.
func (g *Gate) SlotUsage(ctx context.Context, scope ConcurrencyScope) (current int, limit int, err error) {
	key := g.concurrencyKey(scope)
	raw, err := g.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return 0, scope.Limit, nil
		}
		return 0, 0, fmt.Errorf("slot usage %s/%s: %w", scope.Type, scope.Key, err)
	}
	var count int
	if _, err := fmt.Sscanf(raw, "%d", &count); err != nil {
		return 0, scope.Limit, nil
	}
	return count, scope.Limit, nil
}

// ─── Domain Locks ────────────────────────────────────────────────────────────

// acquireLockScript atomically sets a domain lock if none is held.
// Returns 1 if lock acquired, 0 if already held by another agent.
//
// KEYS[1] = lock key
// ARGV[1] = lock data JSON
// ARGV[2] = ttl_seconds
var acquireLockScript = redis.NewScript(`
local existing = redis.call('GET', KEYS[1])
if existing then
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', tonumber(ARGV[2]))
return 1
`)

// AcquireLock attempts to acquire an exclusive lock on a domain surface.
// domain examples: "file:api/orders/", "branch:feat/auth", "service:payments".
// Returns the lock if acquired, nil if already held.
func (g *Gate) AcquireLock(ctx context.Context, domain, holder string, ttl time.Duration) (*DomainLock, error) {
	lockID := fmt.Sprintf("%s:%s:%d", holder, domain, time.Now().UnixMilli())
	lock := DomainLock{
		LockID:     lockID,
		Domain:     domain,
		Holder:     holder,
		AcquiredAt: time.Now().UTC().Format(time.RFC3339),
		TTLSeconds: int(ttl.Seconds()),
	}
	data, err := json.Marshal(lock)
	if err != nil {
		return nil, fmt.Errorf("marshal lock: %w", err)
	}

	result, err := acquireLockScript.Run(ctx, g.rdb,
		[]string{g.lockKey(domain)},
		string(data),
		int(ttl.Seconds()),
	).Int()
	if err != nil {
		return nil, fmt.Errorf("acquire lock %s: %w", domain, err)
	}
	if result == 0 {
		return nil, nil // lock held by another agent
	}

	// Register in the active-locks sorted set for listing
	pipe := g.rdb.Pipeline()
	pipe.ZAdd(ctx, g.key("active-locks"), redis.Z{
		Score:  float64(time.Now().UnixMilli()),
		Member: lock.Domain,
	})
	pipe.Exec(ctx)

	return &lock, nil
}

// ReleaseLock releases a domain lock. Only the holder can release their own lock.
func (g *Gate) ReleaseLock(ctx context.Context, domain, holder string) error {
	key := g.lockKey(domain)
	raw, err := g.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil // already expired
		}
		return fmt.Errorf("get lock %s: %w", domain, err)
	}

	var lock DomainLock
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		return fmt.Errorf("parse lock %s: %w", domain, err)
	}
	if lock.Holder != holder {
		return fmt.Errorf("lock %s held by %s, not %s", domain, lock.Holder, holder)
	}

	pipe := g.rdb.Pipeline()
	pipe.Del(ctx, key)
	pipe.ZRem(ctx, g.key("active-locks"), domain)
	_, err = pipe.Exec(ctx)
	return err
}

// ActiveLocks returns all currently held domain locks (excluding expired ones).
func (g *Gate) ActiveLocks(ctx context.Context) ([]DomainLock, error) {
	members, err := g.rdb.ZRevRange(ctx, g.key("active-locks"), 0, 99).Result()
	if err != nil {
		return nil, fmt.Errorf("list active locks: %w", err)
	}

	var locks []DomainLock
	var toRemove []interface{}
	for _, domain := range members {
		raw, err := g.rdb.Get(ctx, g.lockKey(domain)).Result()
		if err != nil {
			// Lock TTL expired — prune from the set
			toRemove = append(toRemove, domain)
			continue
		}
		var lock DomainLock
		if err := json.Unmarshal([]byte(raw), &lock); err != nil {
			continue
		}
		locks = append(locks, lock)
	}

	// Prune expired entries from the sorted set
	if len(toRemove) > 0 {
		g.rdb.ZRem(ctx, g.key("active-locks"), toRemove...)
	}

	return locks, nil
}

// GetLock returns the current holder of a domain lock, or nil if unheld.
func (g *Gate) GetLock(ctx context.Context, domain string) (*DomainLock, error) {
	raw, err := g.rdb.Get(ctx, g.lockKey(domain)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get lock %s: %w", domain, err)
	}
	var lock DomainLock
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		return nil, fmt.Errorf("parse lock %s: %w", domain, err)
	}
	return &lock, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func (g *Gate) key(suffix string) string {
	return g.ns + ":admission:" + suffix
}

func (g *Gate) lockKey(domain string) string {
	// Sanitize domain to be a safe Redis key segment
	safe := strings.ReplaceAll(domain, " ", "_")
	return g.key("lock:" + safe)
}

func (g *Gate) concurrencyKey(scope ConcurrencyScope) string {
	return g.key("concurrency:" + scope.Type + ":" + scope.Key)
}
