package coordination

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

func testRequestStore(t *testing.T) (*RequestStore, context.Context) {
	t.Helper()

	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}

	ns := "octi-test-req-" + t.Name()
	t.Cleanup(func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})

	return NewRequestStore(rdb, ns), ctx
}

func TestRequestStore_SubmitAndGet(t *testing.T) {
	store, ctx := testRequestStore(t)

	req, err := store.Submit(ctx, "marketing-em", "analytics", RequestReport, "Need weekly PR velocity report", 1, 60)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if req.ID == "" {
		t.Error("expected non-empty ID")
	}
	if req.Status != RequestPending {
		t.Errorf("expected status pending, got %s", req.Status)
	}
	if req.FromAgent != "marketing-em" {
		t.Errorf("FromAgent = %q, want %q", req.FromAgent, "marketing-em")
	}
	if req.ToSquad != "analytics" {
		t.Errorf("ToSquad = %q, want %q", req.ToSquad, "analytics")
	}
	if req.Priority != 1 {
		t.Errorf("Priority = %d, want 1", req.Priority)
	}

	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != req.ID {
		t.Errorf("Get ID = %q, want %q", got.ID, req.ID)
	}
}

func TestRequestStore_Pending(t *testing.T) {
	store, ctx := testRequestStore(t)

	// Submit two requests with different priorities
	_, err := store.Submit(ctx, "agent-a", "cloud", RequestFix, "Fix the CI pipeline", 0, 0) // urgent
	if err != nil {
		t.Fatalf("Submit urgent: %v", err)
	}
	_, err = store.Submit(ctx, "agent-b", "cloud", RequestQuery, "Query DB for stats", 2, 0) // normal
	if err != nil {
		t.Fatalf("Submit normal: %v", err)
	}

	requests, err := store.Pending(ctx, "cloud")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected 2 pending requests, got %d", len(requests))
	}
	// Urgent (priority 0) should come first
	if requests[0].Priority != 0 {
		t.Errorf("first request priority = %d, want 0 (urgent)", requests[0].Priority)
	}
}

func TestRequestStore_Claim(t *testing.T) {
	store, ctx := testRequestStore(t)

	req, err := store.Submit(ctx, "kernel-sr", "cloud", RequestDeploy, "Deploy hotfix to prod", 0, 30)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	claimed, err := store.Claim(ctx, req.ID, "cloud-sr")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.Status != RequestClaimed {
		t.Errorf("status = %s, want claimed", claimed.Status)
	}
	if claimed.ClaimedBy != "cloud-sr" {
		t.Errorf("ClaimedBy = %q, want %q", claimed.ClaimedBy, "cloud-sr")
	}
	if claimed.ClaimedAt == "" {
		t.Error("ClaimedAt should be set")
	}

	// Claiming again should fail
	_, err = store.Claim(ctx, req.ID, "another-agent")
	if err == nil {
		t.Error("expected error when claiming an already-claimed request")
	}
}

func TestRequestStore_Fulfill(t *testing.T) {
	store, ctx := testRequestStore(t)

	req, err := store.Submit(ctx, "studio-em", "analytics", RequestReport, "Need Q1 summary", 1, 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	fulfilled, err := store.Fulfill(ctx, req.ID, "Report at reports/q1-summary.md", 1234)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if fulfilled.Status != RequestFulfilled {
		t.Errorf("status = %s, want fulfilled", fulfilled.Status)
	}
	if fulfilled.Result != "Report at reports/q1-summary.md" {
		t.Errorf("Result = %q", fulfilled.Result)
	}
	if fulfilled.PRNumber != 1234 {
		t.Errorf("PRNumber = %d, want 1234", fulfilled.PRNumber)
	}
	if fulfilled.FulfilledAt == "" {
		t.Error("FulfilledAt should be set")
	}

	// Should be removed from pending queue
	pending, err := store.Pending(ctx, req.ToSquad)
	if err != nil {
		t.Fatalf("Pending after fulfill: %v", err)
	}
	for _, p := range pending {
		if p.ID == req.ID {
			t.Error("fulfilled request should not appear in pending list")
		}
	}

	// Fulfilling again should fail
	_, err = store.Fulfill(ctx, req.ID, "duplicate", 0)
	if err == nil {
		t.Error("expected error when fulfilling an already-fulfilled request")
	}
}

func TestRequestStore_IsOverdue(t *testing.T) {
	store, ctx := testRequestStore(t)

	// deadline of 0 = never overdue
	req, _ := store.Submit(ctx, "a", "b", RequestQuery, "no deadline", 2, 0)
	if req.IsOverdue() {
		t.Error("request with no deadline should not be overdue")
	}

	// deadline of 99999 minutes = not overdue yet
	req2, _ := store.Submit(ctx, "a", "b", RequestQuery, "far future deadline", 2, 99999)
	if req2.IsOverdue() {
		t.Error("request with far future deadline should not be overdue")
	}
}
