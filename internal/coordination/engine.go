package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Claim represents an agent's claim on a task.
type Claim struct {
	ClaimID    string `json:"claim_id"`
	AgentID    string `json:"agent_id"`
	Task       string `json:"task"`
	ClaimedAt  string `json:"claimed_at"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// Signal is a broadcast message from an agent to the swarm.
type Signal struct {
	AgentID   string `json:"agent_id"`
	Type      string `json:"type"` // completed, blocked, need-help, directive, heartbeat
	Payload   string `json:"payload"`
	Timestamp string `json:"timestamp"`
}

// Engine provides agent coordination — claims, signals, status.
type Engine struct {
	rdb *redis.Client
	ns  string
}

// New creates a coordination engine connected to Redis.
func New(redisURL, namespace string) (*Engine, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redis.NewClient(opts)
	return &Engine{rdb: rdb, ns: namespace}, nil
}

// ClaimTask claims a task for an agent. Auto-expires after ttlSeconds.
func (e *Engine) ClaimTask(ctx context.Context, agentID, task string, ttlSeconds int) (*Claim, error) {
	claim := &Claim{
		ClaimID:    fmt.Sprintf("%s:%d", agentID, time.Now().UnixMilli()),
		AgentID:    agentID,
		Task:       task,
		ClaimedAt:  time.Now().UTC().Format(time.RFC3339),
		TTLSeconds: ttlSeconds,
	}
	data, _ := json.Marshal(claim)

	pipe := e.rdb.Pipeline()
	pipe.Set(ctx, e.key("claim:"+agentID), data, time.Duration(ttlSeconds)*time.Second)
	pipe.ZAdd(ctx, e.key("active-claims"), redis.Z{Score: float64(time.Now().UnixMilli()), Member: data})
	_, err := pipe.Exec(ctx)
	return claim, err
}

// defaultClaimTTLSeconds is used to prune zset members whose embedded TTL is
// missing or non-positive (defensive fallback for legacy / malformed claims).
const defaultClaimTTLSeconds = 900

// ActiveClaims returns all non-expired claims across the swarm.
//
// Lazy-prune (issue #206): the underlying `active-claims` zset has no per-member
// TTL — when a holder dies mid-work without calling ReleaseClaim, the entry
// wedged dispatch forever. We now ZREM any member whose score (claimed-at
// UnixMilli) plus its embedded TTL is in the past relative to the Redis server
// clock, AND whose `claim:<agentID>` TTL key is gone. Server time is used so
// that clock skew between octi-pulpo and Redis can't keep dead members alive.
func (e *Engine) ActiveClaims(ctx context.Context) ([]Claim, error) {
	zkey := e.key("active-claims")
	raw, err := e.rdb.ZRangeByScoreWithScores(ctx, zkey, &redis.ZRangeBy{
		Min: "-inf",
		Max: "+inf",
	}).Result()
	if err != nil {
		return nil, err
	}

	// Use Redis server time to avoid local clock skew.
	nowMilli := time.Now().UnixMilli()
	if t, err := e.rdb.Time(ctx).Result(); err == nil {
		nowMilli = t.UnixMilli()
	}

	var claims []Claim
	var stale []interface{}
	for _, z := range raw {
		member, _ := z.Member.(string)
		var c Claim
		if err := json.Unmarshal([]byte(member), &c); err != nil {
			// unparseable garbage — prune.
			stale = append(stale, member)
			continue
		}
		ttl := c.TTLSeconds
		if ttl <= 0 {
			ttl = defaultClaimTTLSeconds
		}
		expiresAtMilli := int64(z.Score) + int64(ttl)*1000

		// The `claim:<agentID>` SET key has its own Redis TTL; if it is gone,
		// the holder either released or the TTL fired. Treat as absent.
		exists, _ := e.rdb.Exists(ctx, e.key("claim:"+c.AgentID)).Result()

		if exists == 0 || expiresAtMilli < nowMilli {
			stale = append(stale, member)
			continue
		}
		claims = append(claims, c)
	}

	if len(stale) > 0 {
		// Best-effort prune; failures here must not break dispatch.
		_ = e.rdb.ZRem(ctx, zkey, stale...).Err()
	}

	// Preserve previous "newest first, capped at 50" semantics.
	if len(claims) > 1 {
		// raw was ascending by score; reverse and cap.
		for i, j := 0, len(claims)-1; i < j; i, j = i+1, j-1 {
			claims[i], claims[j] = claims[j], claims[i]
		}
	}
	if len(claims) > 50 {
		claims = claims[:50]
	}
	return claims, nil
}

// ReleaseClaim explicitly removes an agent's claim before TTL expiry.
// Called by workers when an agent finishes execution.
//
// Fixes #213 (claim-leak): the `active-claims` zset is value-keyed on the
// full claim JSON blob, so a plain ZREM by claim_id silently no-ops and
// wedges dispatch. We look up the exact bytes stored under `claim:<agent>`
// and ZREM that member in the same pipeline as the DEL. If the SET key
// has already expired we skip the ZREM — ActiveClaims' lazy-prune (#206)
// will clean the zset member on the next read.
func (e *Engine) ReleaseClaim(ctx context.Context, agentID string) error {
	claimKey := e.key("claim:" + agentID)
	data, getErr := e.rdb.Get(ctx, claimKey).Bytes()
	if getErr != nil && getErr != redis.Nil {
		return fmt.Errorf("get claim for release: %w", getErr)
	}

	pipe := e.rdb.Pipeline()
	pipe.Del(ctx, claimKey)
	if getErr != redis.Nil && len(data) > 0 {
		pipe.ZRem(ctx, e.key("active-claims"), data)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// Broadcast sends a signal to the swarm via pub/sub.
func (e *Engine) Broadcast(ctx context.Context, agentID, sigType, payload string) error {
	sig := Signal{
		AgentID:   agentID,
		Type:      sigType,
		Payload:   payload,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(sig)

	pipe := e.rdb.Pipeline()
	pipe.ZAdd(ctx, e.key("signals"), redis.Z{Score: float64(time.Now().UnixMilli()), Member: data})
	pipe.ZRemRangeByRank(ctx, e.key("signals"), 0, -501) // keep last 500
	pipe.Publish(ctx, e.ns+":signal-stream", data)
	_, err := pipe.Exec(ctx)
	return err
}

// RecentSignals returns the latest signals from the swarm.
func (e *Engine) RecentSignals(ctx context.Context, limit int) ([]Signal, error) {
	raw, err := e.rdb.ZRevRange(ctx, e.key("signals"), 0, int64(limit)-1).Result()
	if err != nil {
		return nil, err
	}
	var signals []Signal
	for _, r := range raw {
		var s Signal
		if err := json.Unmarshal([]byte(r), &s); err != nil {
			continue
		}
		signals = append(signals, s)
	}
	return signals, nil
}

// Close shuts down the Redis connection.
func (e *Engine) Close() error {
	return e.rdb.Close()
}

func (e *Engine) key(suffix string) string {
	return e.ns + ":" + suffix
}
