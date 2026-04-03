package coordination

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// preflightTestSetup creates a PreflightGate backed by real Redis.
// Tests are skipped gracefully if Redis is not available.
func preflightTestSetup(t *testing.T) (*PreflightGate, context.Context) {
	t.Helper()

	redisURL := "redis://localhost:6379"
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}

	ns := "preflight-test-" + strings.ReplaceAll(t.Name(), "/", "-")

	t.Cleanup(func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})

	return NewPreflightGate(rdb, ns), ctx
}

func TestBlockTransition_BlocksWithNoPhases(t *testing.T) {
	pg, ctx := preflightTestSetup(t)

	allowed, reason := pg.BlockTransition(ctx, "task-1", "assigned", "in_progress")
	if allowed {
		t.Error("expected transition to be blocked with no phases logged")
	}
	for _, phase := range requiredPhases {
		if !strings.Contains(reason, phase) {
			t.Errorf("reason %q should mention missing phase %q", reason, phase)
		}
	}
}

func TestBlockTransition_AllowsAfterAllPhases(t *testing.T) {
	pg, ctx := preflightTestSetup(t)

	for _, phase := range requiredPhases {
		if err := pg.LogPhase(ctx, "task-2", phase); err != nil {
			t.Fatalf("LogPhase(%q): %v", phase, err)
		}
	}

	allowed, reason := pg.BlockTransition(ctx, "task-2", "assigned", "in_progress")
	if !allowed {
		t.Errorf("expected transition to be allowed, got blocked: %s", reason)
	}
}

func TestBlockTransition_BlocksWithPartialPhases(t *testing.T) {
	pg, ctx := preflightTestSetup(t)

	// Log only orient and clarify — missing approach, confirm
	if err := pg.LogPhase(ctx, "task-3", "orient"); err != nil {
		t.Fatalf("LogPhase: %v", err)
	}
	if err := pg.LogPhase(ctx, "task-3", "clarify"); err != nil {
		t.Fatalf("LogPhase: %v", err)
	}

	allowed, reason := pg.BlockTransition(ctx, "task-3", "assigned", "in_progress")
	if allowed {
		t.Error("expected transition to be blocked with only 2 of 4 phases")
	}
	if !strings.Contains(reason, "approach") {
		t.Errorf("reason %q should mention missing 'approach'", reason)
	}
	if !strings.Contains(reason, "confirm") {
		t.Errorf("reason %q should mention missing 'confirm'", reason)
	}
	// Should NOT mention completed phases as missing
	if strings.Contains(reason, "orient") {
		t.Errorf("reason %q should NOT mention completed 'orient' as missing", reason)
	}
}

func TestBlockTransition_PassesThroughOtherTransitions(t *testing.T) {
	pg, ctx := preflightTestSetup(t)

	cases := []struct {
		from string
		to   string
	}{
		{"in_progress", "completed"},
		{"pending", "assigned"},
		{"assigned", "blocked"},
		{"in_progress", "blocked"},
		{"blocked", "assigned"},
	}

	for _, c := range cases {
		allowed, _ := pg.BlockTransition(ctx, "task-any", c.from, c.to)
		if !allowed {
			t.Errorf("transition %s -> %s should pass through, but was blocked", c.from, c.to)
		}
	}
}

func TestLogPhase_Idempotent(t *testing.T) {
	pg, ctx := preflightTestSetup(t)

	// Log the same phase twice — should not error or duplicate
	if err := pg.LogPhase(ctx, "task-4", "orient"); err != nil {
		t.Fatalf("first LogPhase: %v", err)
	}
	if err := pg.LogPhase(ctx, "task-4", "orient"); err != nil {
		t.Fatalf("second LogPhase: %v", err)
	}

	phases, err := pg.CompletedPhases(ctx, "task-4")
	if err != nil {
		t.Fatalf("CompletedPhases: %v", err)
	}
	if len(phases) != 1 {
		t.Errorf("expected 1 phase after idempotent add, got %d", len(phases))
	}
}

func TestCompletedPhases_EmptyForNewTask(t *testing.T) {
	pg, ctx := preflightTestSetup(t)

	phases, err := pg.CompletedPhases(ctx, "task-nonexistent")
	if err != nil {
		t.Fatalf("CompletedPhases: %v", err)
	}
	if len(phases) != 0 {
		t.Errorf("expected 0 phases for new task, got %d", len(phases))
	}
}
