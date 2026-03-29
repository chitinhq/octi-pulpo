package crosssquad_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/crosssquad"
	"github.com/redis/go-redis/v9"
)

// newTestStore creates a Store backed by real Redis on localhost:6379.
// Each test uses a unique namespace to avoid cross-test pollution.
func newTestStore(t *testing.T) *crosssquad.Store {
	t.Helper()
	ns := fmt.Sprintf("test-xsquad-%d", time.Now().UnixNano())
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	t.Cleanup(func() {
		// Clean up all keys created by this test
		ctx := context.Background()
		keys, _ := rdb.Keys(ctx, ns+":xsquad:*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})
	return crosssquad.New(rdb, ns)
}

func TestCreate_ValidRequest(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	req, err := store.Create(ctx, "marketing-em", "analytics", "report", "PR velocity for LinkedIn post", 1, 60)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if req.ID == "" {
		t.Error("expected non-empty ID")
	}
	if req.Status != crosssquad.StatusPending {
		t.Errorf("expected status=%s, got %s", crosssquad.StatusPending, req.Status)
	}
	if req.FromAgent != "marketing-em" || req.ToSquad != "analytics" {
		t.Errorf("unexpected from/to: %s -> %s", req.FromAgent, req.ToSquad)
	}
}

func TestCreate_InvalidType(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Create(ctx, "agent", "squad", "badtype", "desc", 1, 0)
	if err == nil {
		t.Error("expected error for invalid type")
	}
}

func TestList_ReturnsPending(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Create priority-0 first, then priority-1 — list must return 0 first
	store.Create(ctx, "kernel-sr", "cloud", "review", "PR #123", 0, 0)
	store.Create(ctx, "kernel-sr", "cloud", "query", "DB schema for auth", 1, 0)

	requests, err := store.List(ctx, "cloud")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(requests) != 2 {
		t.Errorf("expected 2 requests, got %d", len(requests))
	}
	// ZRange returns by ascending score — priority 0 before priority 1
	if requests[0].Priority != 0 {
		t.Errorf("expected highest-priority request first, got priority=%d", requests[0].Priority)
	}
}

func TestList_ExcludesFulfilled(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	req, _ := store.Create(ctx, "marketing-em", "analytics", "report", "weekly summary", 1, 0)
	store.Fulfill(ctx, req.ID, "report generated at reports/week13.md", 0)

	requests, err := store.List(ctx, "analytics")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(requests) != 0 {
		t.Errorf("expected 0 requests after fulfillment, got %d", len(requests))
	}
}

func TestFulfill_UpdatesStatus(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	req, _ := store.Create(ctx, "kernel-sr", "cloud", "fix", "Neon connection pool leak", 0, 0)
	fulfilled, err := store.Fulfill(ctx, req.ID, "fixed in PR #456", 456)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if fulfilled.Status != crosssquad.StatusFulfilled {
		t.Errorf("expected status=%s, got %s", crosssquad.StatusFulfilled, fulfilled.Status)
	}
	if fulfilled.PRNumber != 456 {
		t.Errorf("expected pr_number=456, got %d", fulfilled.PRNumber)
	}
	if fulfilled.FulfilledAt == "" {
		t.Error("expected fulfilled_at to be set")
	}
}

func TestFulfill_NotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Fulfill(ctx, "req-does-not-exist", "result", 0)
	if err == nil {
		t.Error("expected error for missing request")
	}
}

func TestPendingSquads(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Create(ctx, "kernel-sr", "cloud", "query", "DB help", 1, 0)
	store.Create(ctx, "kernel-sr", "analytics", "report", "metrics", 1, 0)

	squads, err := store.PendingSquads(ctx)
	if err != nil {
		t.Fatalf("PendingSquads: %v", err)
	}
	found := make(map[string]bool)
	for _, s := range squads {
		found[s] = true
	}
	if !found["cloud"] || !found["analytics"] {
		t.Errorf("expected cloud and analytics in pending squads, got %v", squads)
	}
}

func TestList_EmptySquad(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	requests, err := store.List(ctx, "no-such-squad")
	if err != nil {
		t.Fatalf("List on empty squad: %v", err)
	}
	if len(requests) != 0 {
		t.Errorf("expected 0 requests for empty squad, got %d", len(requests))
	}
}
