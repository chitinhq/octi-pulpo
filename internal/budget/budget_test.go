package budget

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

func budgetTestSetup(t *testing.T) (*BudgetStore, context.Context) {
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

	ns := "octi-test-budget-" + t.Name()

	cleanup := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	cleanup()
	t.Cleanup(func() {
		cleanup()
		rdb.Close()
	})

	return NewBudgetStore(rdb, ns), ctx
}

func TestCheckAndIncrement_Allowed(t *testing.T) {
	bs, ctx := budgetTestSetup(t)

	budget := AgentBudget{
		Agent:              "sr-kernel-01",
		Driver:             "claude-code",
		Box:                "jared",
		BudgetMonthlyCents: 770,
		SpentMonthlyCents:  100,
		RunsThisMonth:      5,
		Paused:             false,
	}
	if err := bs.SetBudget(ctx, budget); err != nil {
		t.Fatalf("set budget: %v", err)
	}

	allowed, err := bs.CheckAndIncrement(ctx, "sr-kernel-01", 50, "NORMAL")
	if err != nil {
		t.Fatalf("check and increment: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed=true for agent with remaining budget")
	}

	// Verify spent was incremented
	got, err := bs.GetBudget(ctx, "sr-kernel-01")
	if err != nil {
		t.Fatalf("get budget: %v", err)
	}
	if got.SpentMonthlyCents != 150 {
		t.Errorf("expected spent=150, got %d", got.SpentMonthlyCents)
	}
	if got.RunsThisMonth != 6 {
		t.Errorf("expected runs=6, got %d", got.RunsThisMonth)
	}
	if got.LastRunAt == "" {
		t.Error("expected last_run_at to be set")
	}
}

func TestCheckAndIncrement_Exhausted(t *testing.T) {
	bs, ctx := budgetTestSetup(t)

	budget := AgentBudget{
		Agent:              "sr-kernel-02",
		Driver:             "claude-code",
		Box:                "jared",
		BudgetMonthlyCents: 100,
		SpentMonthlyCents:  100,
		RunsThisMonth:      10,
		Paused:             false,
	}
	if err := bs.SetBudget(ctx, budget); err != nil {
		t.Fatalf("set budget: %v", err)
	}

	allowed, err := bs.CheckAndIncrement(ctx, "sr-kernel-02", 50, "NORMAL")
	if err != nil {
		t.Fatalf("check and increment: %v", err)
	}
	if allowed {
		t.Fatal("expected allowed=false for exhausted budget")
	}

	// Verify auto-pause was set
	got, err := bs.GetBudget(ctx, "sr-kernel-02")
	if err != nil {
		t.Fatalf("get budget: %v", err)
	}
	if !got.Paused {
		t.Error("expected paused=true after exhaustion")
	}
}

func TestCheckAndIncrement_CriticalBypass(t *testing.T) {
	bs, ctx := budgetTestSetup(t)

	budget := AgentBudget{
		Agent:              "sr-kernel-03",
		Driver:             "claude-code",
		Box:                "jared",
		BudgetMonthlyCents: 100,
		SpentMonthlyCents:  100,
		RunsThisMonth:      10,
		Paused:             true,
	}
	if err := bs.SetBudget(ctx, budget); err != nil {
		t.Fatalf("set budget: %v", err)
	}

	allowed, err := bs.CheckAndIncrement(ctx, "sr-kernel-03", 50, "CRITICAL")
	if err != nil {
		t.Fatalf("check and increment: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed=true for CRITICAL priority (bypasses everything)")
	}

	// Verify spent was still incremented
	got, err := bs.GetBudget(ctx, "sr-kernel-03")
	if err != nil {
		t.Fatalf("get budget: %v", err)
	}
	if got.SpentMonthlyCents != 150 {
		t.Errorf("expected spent=150, got %d", got.SpentMonthlyCents)
	}
}

func TestCheckAndIncrement_PriorityThresholds(t *testing.T) {
	bs, ctx := budgetTestSetup(t)

	// 1000 budget, 750 spent → 25% remaining
	// BACKGROUND threshold = 0.50 → need 50% remaining → denied
	// NORMAL threshold = 0.30 → need 30% remaining → denied
	// HIGH threshold = 0.15 → need 15% remaining → allowed (25% > 15%)

	base := AgentBudget{
		Agent:              "sr-kernel-04",
		Driver:             "claude-code",
		Box:                "jared",
		BudgetMonthlyCents: 1000,
		SpentMonthlyCents:  750,
		RunsThisMonth:      20,
		Paused:             false,
	}

	// Test BACKGROUND — should be denied
	if err := bs.SetBudget(ctx, base); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	allowed, err := bs.CheckAndIncrement(ctx, "sr-kernel-04", 10, "BACKGROUND")
	if err != nil {
		t.Fatalf("check BACKGROUND: %v", err)
	}
	if allowed {
		t.Error("BACKGROUND should be denied at 25% remaining (threshold 50%)")
	}

	// Reset spent back (the denied call should not have changed spent)
	got, _ := bs.GetBudget(ctx, "sr-kernel-04")
	if got.SpentMonthlyCents != 750 {
		t.Errorf("BACKGROUND denial should not change spent, got %d", got.SpentMonthlyCents)
	}

	// Test NORMAL — should be denied
	if err := bs.SetBudget(ctx, base); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	allowed, err = bs.CheckAndIncrement(ctx, "sr-kernel-04", 10, "NORMAL")
	if err != nil {
		t.Fatalf("check NORMAL: %v", err)
	}
	if allowed {
		t.Error("NORMAL should be denied at 25% remaining (threshold 30%)")
	}

	// Test HIGH — should be allowed
	if err := bs.SetBudget(ctx, base); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	allowed, err = bs.CheckAndIncrement(ctx, "sr-kernel-04", 10, "HIGH")
	if err != nil {
		t.Fatalf("check HIGH: %v", err)
	}
	if !allowed {
		t.Error("HIGH should be allowed at 25% remaining (threshold 15%)")
	}
}

func TestListAll(t *testing.T) {
	bs, ctx := budgetTestSetup(t)

	// Empty namespace returns empty slice, not error.
	all, err := bs.ListAll(ctx)
	if err != nil {
		t.Fatalf("list all empty: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected 0 budgets, got %d", len(all))
	}

	agents := []AgentBudget{
		{Agent: "sr-list-01", Driver: "claude-code", BudgetMonthlyCents: 500},
		{Agent: "sr-list-02", Driver: "codex", BudgetMonthlyCents: 300},
		{Agent: "sr-list-03", Driver: "ollama", BudgetMonthlyCents: 0, Paused: true},
	}
	for _, b := range agents {
		if b.BudgetMonthlyCents == 0 {
			// Store zero-budget agents directly (SetBudget doesn't validate)
			b.BudgetMonthlyCents = 1 // minimal valid value for storage
		}
		if err := bs.SetBudget(ctx, b); err != nil {
			t.Fatalf("set budget %s: %v", b.Agent, err)
		}
	}

	all, err = bs.ListAll(ctx)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 budgets, got %d", len(all))
	}

	// Verify each agent appears exactly once.
	found := make(map[string]bool)
	for _, b := range all {
		found[b.Agent] = true
	}
	for _, b := range agents {
		if !found[b.Agent] {
			t.Errorf("agent %s missing from ListAll result", b.Agent)
		}
	}
}

func TestMonthlyReset(t *testing.T) {
	bs, ctx := budgetTestSetup(t)

	budget := AgentBudget{
		Agent:              "sr-kernel-05",
		Driver:             "claude-code",
		Box:                "jared",
		BudgetMonthlyCents: 500,
		SpentMonthlyCents:  450,
		RunsThisMonth:      30,
		LastRunAt:          "2026-03-15T10:00:00Z",
		Paused:             true,
	}
	if err := bs.SetBudget(ctx, budget); err != nil {
		t.Fatalf("set budget: %v", err)
	}

	if err := bs.MonthlyReset(ctx, "sr-kernel-05"); err != nil {
		t.Fatalf("monthly reset: %v", err)
	}

	got, err := bs.GetBudget(ctx, "sr-kernel-05")
	if err != nil {
		t.Fatalf("get budget: %v", err)
	}

	if got.SpentMonthlyCents != 0 {
		t.Errorf("expected spent=0 after reset, got %d", got.SpentMonthlyCents)
	}
	if got.RunsThisMonth != 0 {
		t.Errorf("expected runs=0 after reset, got %d", got.RunsThisMonth)
	}
	if got.Paused {
		t.Error("expected paused=false after reset")
	}
	// Budget and other fields should be preserved
	if got.BudgetMonthlyCents != 500 {
		t.Errorf("expected budget=500 preserved, got %d", got.BudgetMonthlyCents)
	}
	if got.Agent != "sr-kernel-05" {
		t.Errorf("expected agent name preserved, got %s", got.Agent)
	}
}
