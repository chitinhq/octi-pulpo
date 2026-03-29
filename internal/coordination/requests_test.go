package coordination

import (
	"context"
	"os"
	"strings"
	"testing"
)

// newTestEngine creates a coordination engine for tests.
// Skips if Redis is unavailable.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	eng, err := New(redisURL, "octi-test-"+strings.ReplaceAll(t.Name(), "/", "-"))
	if err != nil {
		t.Skipf("skipping: cannot connect to redis: %v", err)
	}
	ctx := context.Background()
	if err := eng.rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

func TestSubmitAndCheckRequests(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	req, err := eng.SubmitRequest(ctx, "marketing-em", "analytics", RequestTypeReport,
		"Need weekly PR velocity report for LinkedIn post", 1, 0)
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}
	if req.ID == "" {
		t.Fatal("expected non-empty request ID")
	}
	if req.Status != RequestStatusPending {
		t.Fatalf("expected pending status, got %s", req.Status)
	}

	pending, err := eng.GetPendingRequests(ctx, "analytics")
	if err != nil {
		t.Fatalf("GetPendingRequests: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("expected at least one pending request")
	}

	found := false
	for _, p := range pending {
		if p.ID == req.ID {
			found = true
			if p.FromAgent != "marketing-em" {
				t.Errorf("from_agent: want marketing-em, got %s", p.FromAgent)
			}
			if p.Type != RequestTypeReport {
				t.Errorf("type: want report, got %s", p.Type)
			}
		}
	}
	if !found {
		t.Errorf("submitted request %s not found in pending list", req.ID)
	}
}

func TestFulfillRequest(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	req, err := eng.SubmitRequest(ctx, "kernel-sr", "analytics", RequestTypeQuery,
		"How many PRs merged last week?", 0, 60)
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}

	err = eng.FulfillRequest(ctx, req.ID, "analytics-sr", "42 PRs merged. See report at reports/week13.md", 0)
	if err != nil {
		t.Fatalf("FulfillRequest: %v", err)
	}

	// Fulfilled request should not appear in pending list.
	pending, err := eng.GetPendingRequests(ctx, "analytics")
	if err != nil {
		t.Fatalf("GetPendingRequests: %v", err)
	}
	for _, p := range pending {
		if p.ID == req.ID {
			t.Errorf("fulfilled request %s still appears in pending list", req.ID)
		}
	}
}

func TestFulfillRequestNotFound(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	err := eng.FulfillRequest(ctx, "req-nonexistent-0", "analytics-sr", "done", 0)
	if err == nil {
		t.Fatal("expected error for nonexistent request ID")
	}
}

func TestFulfillRequestAlreadyFulfilled(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	req, err := eng.SubmitRequest(ctx, "cloud-em", "kernel", RequestTypeFix,
		"Fix the flaky CI test on PR #1400", 1, 120)
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}

	if err := eng.FulfillRequest(ctx, req.ID, "kernel-sr", "Fixed in PR #1401", 1401); err != nil {
		t.Fatalf("first FulfillRequest: %v", err)
	}
	if err := eng.FulfillRequest(ctx, req.ID, "kernel-sr", "duplicate", 0); err == nil {
		t.Fatal("expected error on double-fulfillment")
	}
}
