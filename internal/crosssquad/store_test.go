package crosssquad

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

// testStore creates a Store backed by real Redis with an isolated namespace.
// Tests are skipped if Redis is unavailable.
func testStore(t *testing.T) (*Store, context.Context) {
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

	ns := "octi-test-crosssquad-" + t.Name()
	cleanKeys := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	t.Cleanup(func() {
		cleanKeys()
		rdb.Close()
	})
	cleanKeys() // flush any leftovers from a previous run

	return New(rdb, ns), ctx
}

func TestCreate_StoresRequest(t *testing.T) {
	s, ctx := testStore(t)

	req, err := s.Create(ctx, "marketing-em", "analytics", RequestTypeReport,
		"Need weekly PR velocity report", 1, 60)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if req.ID == "" {
		t.Error("expected non-empty ID")
	}
	if req.Status != StatusPending {
		t.Errorf("expected pending, got %s", req.Status)
	}
	if req.ToSquad != "analytics" {
		t.Errorf("expected analytics, got %s", req.ToSquad)
	}
	if req.DeadlineAt == "" {
		t.Error("expected deadline_at set when deadline_minutes > 0")
	}
}

func TestGetBySquad_ReturnsRequests(t *testing.T) {
	s, ctx := testStore(t)

	_, err := s.Create(ctx, "kernel-sr", "cloud", RequestTypeQuery,
		"Get DB connection count", 2, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	reqs, err := s.GetBySquad(ctx, "cloud")
	if err != nil {
		t.Fatalf("GetBySquad: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].FromAgent != "kernel-sr" {
		t.Errorf("expected kernel-sr, got %s", reqs[0].FromAgent)
	}
}

func TestGetBySquad_Empty(t *testing.T) {
	s, ctx := testStore(t)

	reqs, err := s.GetBySquad(ctx, "nonexistent-squad")
	if err != nil {
		t.Fatalf("GetBySquad: %v", err)
	}
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests, got %d", len(reqs))
	}
}

func TestFulfill_UpdatesStatus(t *testing.T) {
	s, ctx := testStore(t)

	req, err := s.Create(ctx, "marketing-em", "analytics", RequestTypeReport,
		"PR velocity report", 1, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	err = s.Fulfill(ctx, req.ID, "analytics-reporter", "report at reports/pr-velocity.md", 1415)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}

	reqs, err := s.GetBySquad(ctx, "analytics")
	if err != nil {
		t.Fatalf("GetBySquad: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	got := reqs[0]
	if got.Status != StatusFulfilled {
		t.Errorf("expected fulfilled, got %s", got.Status)
	}
	if got.FulfilledBy != "analytics-reporter" {
		t.Errorf("expected analytics-reporter, got %s", got.FulfilledBy)
	}
	if got.PRNumber != 1415 {
		t.Errorf("expected PR 1415, got %d", got.PRNumber)
	}
	if got.Result == "" {
		t.Error("expected non-empty result")
	}
}

func TestFulfill_NotFound(t *testing.T) {
	s, ctx := testStore(t)

	err := s.Fulfill(ctx, "req-nonexistent", "agent", "result", 0)
	if err == nil {
		t.Error("expected error for nonexistent request ID")
	}
}

func TestGetBySquad_UrgentFirst(t *testing.T) {
	s, ctx := testStore(t)

	// Create normal priority first, then urgent — urgent should appear first.
	_, err := s.Create(ctx, "agent-a", "analytics", RequestTypeReport, "normal request", 2, 0)
	if err != nil {
		t.Fatalf("Create normal: %v", err)
	}
	_, err = s.Create(ctx, "agent-b", "analytics", RequestTypeReport, "urgent request", 0, 0)
	if err != nil {
		t.Fatalf("Create urgent: %v", err)
	}

	reqs, err := s.GetBySquad(ctx, "analytics")
	if err != nil {
		t.Fatalf("GetBySquad: %v", err)
	}
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	if reqs[0].Description != "urgent request" {
		t.Errorf("expected urgent first, got %q", reqs[0].Description)
	}
}

func TestAgeMinutes_Valid(t *testing.T) {
	s, ctx := testStore(t)

	req, err := s.Create(ctx, "a", "b", RequestTypeQuery, "test", 2, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	age := req.AgeMinutes()
	if age < 0 || age > 1 {
		t.Errorf("expected age ~0, got %d", age)
	}
}
