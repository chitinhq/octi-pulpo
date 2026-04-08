package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ProgressSnapshot captures a moment of work activity from a worker.
type ProgressSnapshot struct {
	Timestamp  time.Time `json:"ts"`
	WorkerID   string    `json:"worker_id"`
	ContractID string    `json:"contract_id"`
	Action     string    `json:"action"` // "tool_start" | "tool_complete" | "milestone"
	Tool       string    `json:"tool"`
	Summary    string    `json:"summary"`
}

// progressKey returns the Redis stream key for a contract's progress.
func progressKey(namespace, contractID string) string {
	return fmt.Sprintf("%s:progress:%s", namespace, contractID)
}

// PublishProgress publishes a snapshot to a Redis stream.
// Stream key: {namespace}:progress:{contractID}
// Auto-trims to ~1000 entries. Sets 1-hour TTL on first write.
func PublishProgress(ctx context.Context, rdb *redis.Client, namespace string, snap ProgressSnapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	key := progressKey(namespace, snap.ContractID)

	// XADD returns the new entry ID.
	id, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: key,
		Values: map[string]interface{}{"data": string(data)},
	}).Result()
	if err != nil {
		return fmt.Errorf("xadd progress: %w", err)
	}

	// Trim to ~1000 entries (approximate).
	pipe := rdb.Pipeline()
	pipe.XTrimMaxLenApprox(ctx, key, 1000, 0)

	// Set TTL only on first write (when the stream had no entries before this one).
	// We detect first write by checking whether the entry we just added is the
	// very first one in the stream (its ID equals the result of XRANGE ... COUNT 1).
	// Simpler heuristic: check stream length after add; if it's 1, set TTL.
	pipe.XLen(ctx, key)

	results, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("trim/len pipeline: %w", err)
	}

	// results[1] is the XLen result.
	if lenResult, ok := results[1].(*redis.IntCmd); ok {
		if lenResult.Val() == 1 {
			// First entry — set 1-hour TTL.
			if err := rdb.Expire(ctx, key, time.Hour).Err(); err != nil {
				return fmt.Errorf("expire progress stream: %w", err)
			}
		}
	}

	_ = id
	return nil
}

// ReadProgress reads new snapshots from a contract's stream.
// lastID should be "0" for first read, or the last seen ID for subsequent reads.
// Returns snapshots and the new lastID to use next time.
func ReadProgress(ctx context.Context, rdb *redis.Client, namespace string, contractID string, lastID string) ([]ProgressSnapshot, string, error) {
	key := progressKey(namespace, contractID)

	// XRANGE key lastID + reads from lastID (exclusive when lastID came from a
	// previous read — callers pass the last returned ID back, so we use the
	// standard Redis convention of incrementing the sequence number by passing
	// the ID as-is; XRANGE treats the range as inclusive so we need to exclude
	// the already-seen entry on subsequent reads).
	// To implement exclusive-start after first read, callers should pass the
	// last ID they received. We handle exclusion by using XRANGE with the
	// "(lastID" exclusive syntax only when lastID != "0".
	var start string
	if lastID == "0" {
		start = "0"
	} else {
		// Increment the sequence number to exclude the last seen entry.
		start = incrementStreamID(lastID)
	}

	entries, err := rdb.XRange(ctx, key, start, "+").Result()
	if err != nil {
		return nil, lastID, fmt.Errorf("xrange progress: %w", err)
	}

	snaps := make([]ProgressSnapshot, 0, len(entries))
	newLastID := lastID

	for _, entry := range entries {
		raw, ok := entry.Values["data"]
		if !ok {
			continue
		}
		var snap ProgressSnapshot
		if err := json.Unmarshal([]byte(fmt.Sprint(raw)), &snap); err != nil {
			continue
		}
		snaps = append(snaps, snap)
		newLastID = entry.ID
	}

	return snaps, newLastID, nil
}

// DetectGap returns true if no snapshot has been published for the given contract
// within the threshold duration.
func DetectGap(ctx context.Context, rdb *redis.Client, namespace string, contractID string, threshold time.Duration) (bool, error) {
	key := progressKey(namespace, contractID)

	entries, err := rdb.XRevRange(ctx, key, "+", "-").Result()
	if err != nil {
		return false, fmt.Errorf("xrevrange progress: %w", err)
	}

	// No entries at all — gap detected.
	if len(entries) == 0 {
		return true, nil
	}

	// Parse the most recent snapshot to get its timestamp.
	latest := entries[0]
	raw, ok := latest.Values["data"]
	if !ok {
		return true, nil
	}

	var snap ProgressSnapshot
	if err := json.Unmarshal([]byte(fmt.Sprint(raw)), &snap); err != nil {
		return true, nil
	}

	age := time.Since(snap.Timestamp)
	return age > threshold, nil
}

// incrementStreamID increments the sequence number of a Redis stream ID
// (format: <ms>-<seq>) so that XRANGE excludes the entry with that ID.
func incrementStreamID(id string) string {
	var ms, seq uint64
	if n, _ := fmt.Sscanf(id, "%d-%d", &ms, &seq); n == 2 {
		return fmt.Sprintf("%d-%d", ms, seq+1)
	}
	// Fallback: return id unchanged (XRANGE will include it, harmless duplication).
	return id
}
