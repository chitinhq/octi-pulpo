// Package optimize provides cost optimization for API-driven dispatch.
package optimize

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Default TTLs for result caching by task type.
var DefaultTTLs = map[string]time.Duration{
	"triage":    1 * time.Hour,
	"pr-review": 15 * time.Minute,
	"qa":        30 * time.Minute,
	"code-gen":  5 * time.Minute,  // code changes fast
	"bugfix":    5 * time.Minute,
}

const defaultTTL = 10 * time.Minute

// CachedResult is a stored dispatch result.
type CachedResult struct {
	Output    string `json:"output"`
	Status    string `json:"status"`
	Adapter   string `json:"adapter"`
	CostCents int    `json:"cost_cents"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	CachedAt  string `json:"cached_at"`
}

// Dedup checks Redis for cached results before dispatching.
type Dedup struct {
	rdb       *redis.Client
	namespace string
	ttls      map[string]time.Duration
}

// NewDedup creates a dedup cache backed by Redis.
func NewDedup(rdb *redis.Client, namespace string) *Dedup {
	return &Dedup{
		rdb:       rdb,
		namespace: namespace,
		ttls:      DefaultTTLs,
	}
}

// TaskHash computes a deterministic SHA-256 hash for a task.
func TaskHash(taskType, prompt, repo string) string {
	h := sha256.New()
	h.Write([]byte(taskType))
	h.Write([]byte{0})
	h.Write([]byte(prompt))
	h.Write([]byte{0})
	h.Write([]byte(repo))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// Check returns a cached result if one exists and is fresh.
func (d *Dedup) Check(ctx context.Context, taskType, prompt, repo string) (*CachedResult, bool) {
	hash := TaskHash(taskType, prompt, repo)
	key := d.key(hash)

	raw, err := d.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, false
	}

	var cached CachedResult
	if json.Unmarshal([]byte(raw), &cached) != nil {
		return nil, false
	}

	return &cached, true
}

// Store caches a dispatch result with type-appropriate TTL.
func (d *Dedup) Store(ctx context.Context, taskType, prompt, repo string, result *CachedResult) error {
	hash := TaskHash(taskType, prompt, repo)
	key := d.key(hash)

	result.CachedAt = time.Now().UTC().Format(time.RFC3339)

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal cached result: %w", err)
	}

	ttl := d.ttlFor(taskType)
	return d.rdb.Set(ctx, key, data, ttl).Err()
}

// Stats returns hit/miss counts for observability.
func (d *Dedup) Stats(ctx context.Context) (hits, total int64, err error) {
	hits, err = d.rdb.Get(ctx, d.namespace+":dedup:hits").Int64()
	if err != nil && err != redis.Nil {
		return 0, 0, err
	}
	total, err = d.rdb.Get(ctx, d.namespace+":dedup:total").Int64()
	if err != nil && err != redis.Nil {
		return 0, 0, err
	}
	return hits, total, nil
}

// RecordHit increments the hit counter.
func (d *Dedup) RecordHit(ctx context.Context) {
	d.rdb.Incr(ctx, d.namespace+":dedup:hits")
	d.rdb.Incr(ctx, d.namespace+":dedup:total")
}

// RecordMiss increments the total counter (miss = total - hits).
func (d *Dedup) RecordMiss(ctx context.Context) {
	d.rdb.Incr(ctx, d.namespace+":dedup:total")
}

func (d *Dedup) ttlFor(taskType string) time.Duration {
	if ttl, ok := d.ttls[taskType]; ok {
		return ttl
	}
	return defaultTTL
}

func (d *Dedup) key(hash string) string {
	return d.namespace + ":dedup:" + hash
}
