package coordination

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const testRedisURL = "redis://localhost:6379"

// newTestRedis returns a Redis client for tests. If Redis is unavailable the
// test is skipped.
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(testRedisURL)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

// uniqueNS returns a namespace unique to this test run to avoid key collisions.
func uniqueNS(t *testing.T) string {
	return fmt.Sprintf("test:%s:%d", t.Name(), time.Now().UnixNano())
}

func makeSnap(workerID, contractID, action string) ProgressSnapshot {
	return ProgressSnapshot{
		Timestamp:  time.Now().UTC(),
		WorkerID:   workerID,
		ContractID: contractID,
		Action:     action,
		Tool:       "bash",
		Summary:    "test summary",
	}
}

// TestPublishProgress verifies that a published snapshot can be read back with
// all fields intact.
func TestPublishProgress(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	ns := uniqueNS(t)

	snap := makeSnap("worker-1", "contract-abc", "tool_start")

	if err := PublishProgress(ctx, rdb, ns, snap); err != nil {
		t.Fatalf("PublishProgress: %v", err)
	}

	snaps, _, err := ReadProgress(ctx, rdb, ns, snap.ContractID, "0")
	if err != nil {
		t.Fatalf("ReadProgress: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}

	got := snaps[0]
	if got.WorkerID != snap.WorkerID {
		t.Errorf("WorkerID: got %q, want %q", got.WorkerID, snap.WorkerID)
	}
	if got.ContractID != snap.ContractID {
		t.Errorf("ContractID: got %q, want %q", got.ContractID, snap.ContractID)
	}
	if got.Action != snap.Action {
		t.Errorf("Action: got %q, want %q", got.Action, snap.Action)
	}
	if got.Tool != snap.Tool {
		t.Errorf("Tool: got %q, want %q", got.Tool, snap.Tool)
	}
	if got.Summary != snap.Summary {
		t.Errorf("Summary: got %q, want %q", got.Summary, snap.Summary)
	}
}

// TestReadProgress_FromLastID publishes 3 snapshots, reads from after the first,
// and expects only the last 2.
func TestReadProgress_FromLastID(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	ns := uniqueNS(t)
	contractID := "contract-xyz"

	// Publish 3 snapshots.
	for i := 0; i < 3; i++ {
		snap := makeSnap(fmt.Sprintf("worker-%d", i), contractID, "milestone")
		if err := PublishProgress(ctx, rdb, ns, snap); err != nil {
			t.Fatalf("PublishProgress[%d]: %v", i, err)
		}
	}

	// First read — get all 3 and note the ID after the first.
	all, lastID, err := ReadProgress(ctx, rdb, ns, contractID, "0")
	if err != nil {
		t.Fatalf("ReadProgress all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 snapshots on first read, got %d", len(all))
	}

	// Simulate reading from after the first entry by re-reading from "0",
	// grabbing the first entry's ID, then reading from that ID.
	firstID, _, err := ReadProgress(ctx, rdb, ns, contractID, "0")
	if err != nil {
		t.Fatalf("ReadProgress for first ID: %v", err)
	}
	_ = firstID

	// We need the stream ID of the first entry. Re-read raw to get it.
	key := progressKey(ns, contractID)
	entries, err := rdb.XRange(ctx, key, "0", "+").Result()
	if err != nil {
		t.Fatalf("XRange: %v", err)
	}
	afterFirstID := entries[0].ID

	// Read from after the first entry.
	rest, newLastID, err := ReadProgress(ctx, rdb, ns, contractID, afterFirstID)
	if err != nil {
		t.Fatalf("ReadProgress from afterFirst: %v", err)
	}
	if len(rest) != 2 {
		t.Errorf("expected 2 snapshots after first, got %d", len(rest))
	}
	if newLastID == afterFirstID {
		t.Errorf("lastID did not advance")
	}
	_ = lastID
}

// TestDetectGap_NoRecent verifies that an empty stream reports a gap.
func TestDetectGap_NoRecent(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	ns := uniqueNS(t)

	gap, err := DetectGap(ctx, rdb, ns, "contract-empty", 30*time.Second)
	if err != nil {
		t.Fatalf("DetectGap: %v", err)
	}
	if !gap {
		t.Error("expected gap for empty stream, got false")
	}
}

// TestDetectGap_Recent verifies that a freshly published snapshot does not
// trigger a gap.
func TestDetectGap_Recent(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	ns := uniqueNS(t)
	contractID := "contract-fresh"

	snap := makeSnap("worker-1", contractID, "tool_complete")
	if err := PublishProgress(ctx, rdb, ns, snap); err != nil {
		t.Fatalf("PublishProgress: %v", err)
	}

	gap, err := DetectGap(ctx, rdb, ns, contractID, 30*time.Second)
	if err != nil {
		t.Fatalf("DetectGap: %v", err)
	}
	if gap {
		t.Error("expected no gap for fresh snapshot, got true")
	}
}

// TestPublishProgress_Trims publishes 1500 entries and verifies the stream is
// trimmed to approximately 1000 entries.
func TestPublishProgress_Trims(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	ns := uniqueNS(t)
	contractID := "contract-trim"

	for i := 0; i < 1500; i++ {
		snap := makeSnap("worker-1", contractID, "tool_start")
		if err := PublishProgress(ctx, rdb, ns, snap); err != nil {
			t.Fatalf("PublishProgress[%d]: %v", i, err)
		}
	}

	key := progressKey(ns, contractID)
	length, err := rdb.XLen(ctx, key).Result()
	if err != nil {
		t.Fatalf("XLen: %v", err)
	}

	// MAXLEN ~ 1000 is approximate; allow up to 20% over.
	const maxAllowed = 1200
	if length > maxAllowed {
		t.Errorf("stream length %d exceeds allowed max %d after 1500 publishes", length, maxAllowed)
	}
	if length < 900 {
		t.Errorf("stream length %d is suspiciously low (< 900)", length)
	}
	t.Logf("stream length after 1500 publishes: %d", length)
}
