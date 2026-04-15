package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/redis/go-redis/v9"
)

// TierBucket is one row of the tier_activity summary.
type TierBucket struct {
	Dispatches int    `json:"dispatches"`
	LastAt     string `json:"last_at,omitempty"`
}

// TierActivitySummary is the shape returned by the tier_activity MCP tool.
type TierActivitySummary struct {
	WindowHours int                    `json:"window_hours"`
	Scanned     int                    `json:"scanned"`
	Tiers       map[string]*TierBucket `json:"tiers"`
}

// knownTiers is the canonical tier set reported by v0. Buckets are always
// present (dispatches=0) so clients get a stable shape.
// knownTiers covers the active Ladder Forge surfaces after the 2026-04-15
// retirement of the old T3 "cloud-scheduled" (RemoteTrigger) bucket. Any
// legacy records still carrying Tier="cloud" in Redis will be surfaced
// dynamically in the output (see bucket creation below) but are no longer
// pre-initialized.
var knownTiers = []string{"local", "actions", "copilot", "desktop", "human", "unknown"}

// tierActivitySummary scans the last `limit` entries of the dispatch log in
// Redis namespace `ns` and groups them by tier over the last `windowHours`.
func tierActivitySummary(ctx context.Context, rdb *redis.Client, ns string, windowHours, limit int) (*TierActivitySummary, error) {
	key := ns + ":dispatch-log"
	raw, err := rdb.LRange(ctx, key, 0, int64(limit)-1).Result()
	if err != nil {
		return nil, err
	}

	summary := &TierActivitySummary{
		WindowHours: windowHours,
		Tiers:       make(map[string]*TierBucket, len(knownTiers)),
	}
	for _, t := range knownTiers {
		summary.Tiers[t] = &TierBucket{}
	}

	cutoff := time.Now().UTC().Add(-time.Duration(windowHours) * time.Hour)

	for _, entry := range raw {
		var rec dispatch.DispatchRecord
		if err := json.Unmarshal([]byte(entry), &rec); err != nil {
			continue
		}

		ts, tsErr := time.Parse(time.RFC3339, rec.Timestamp)
		if tsErr == nil && ts.Before(cutoff) {
			continue
		}
		summary.Scanned++

		tier := rec.Tier
		if tier == "" {
			tier = "unknown"
		}
		bucket, ok := summary.Tiers[tier]
		if !ok {
			bucket = &TierBucket{}
			summary.Tiers[tier] = bucket
		}
		bucket.Dispatches++
		if tsErr == nil {
			if bucket.LastAt == "" {
				bucket.LastAt = rec.Timestamp
			} else if prev, err := time.Parse(time.RFC3339, bucket.LastAt); err == nil && ts.After(prev) {
				bucket.LastAt = rec.Timestamp
			}
		}
	}

	return summary, nil
}
