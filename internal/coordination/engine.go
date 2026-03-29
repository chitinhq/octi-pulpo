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

// ActiveClaims returns all non-expired claims across the swarm.
func (e *Engine) ActiveClaims(ctx context.Context) ([]Claim, error) {
	raw, err := e.rdb.ZRevRange(ctx, e.key("active-claims"), 0, 50).Result()
	if err != nil {
		return nil, err
	}
	var claims []Claim
	for _, r := range raw {
		var c Claim
		if err := json.Unmarshal([]byte(r), &c); err != nil {
			continue
		}
		// Check if the claim TTL key still exists
		exists, _ := e.rdb.Exists(ctx, e.key("claim:"+c.AgentID)).Result()
		if exists > 0 {
			claims = append(claims, c)
		}
	}
	return claims, nil
}

// ReleaseClaim explicitly removes an agent's claim before TTL expiry.
// Called by workers when an agent finishes execution.
func (e *Engine) ReleaseClaim(ctx context.Context, agentID string) error {
	return e.rdb.Del(ctx, e.key("claim:"+agentID)).Err()
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
