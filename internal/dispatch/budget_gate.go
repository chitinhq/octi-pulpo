package dispatch

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/AgentGuardHQ/octi-pulpo/internal/budget"
)

// pipelineAgents are the agent names used by Claude API handlers.
// Total spend across all of them is checked against the monthly cap.
var pipelineAgents = []string{"triage", "planner", "reviewer"}

// budgetDefaults
const (
	defaultMonthlyCapCents = 5000 // $50
	warnThresholdPct       = 80   // log warning at 80%
	pauseThresholdPct      = 95   // pause all calls at 95%
)

// monthlyCapCents reads the monthly budget cap from CLAUDE_BUDGET_MONTHLY env
// var (in cents). Falls back to defaultMonthlyCapCents.
func monthlyCapCents() int {
	if v := os.Getenv("CLAUDE_BUDGET_MONTHLY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMonthlyCapCents
}

// totalPipelineSpend sums SpentMonthlyCents across all pipeline agents.
// Missing agents (no budget record yet) contribute 0.
func totalPipelineSpend(ctx context.Context, bs *budget.BudgetStore) int {
	var total int
	for _, agent := range pipelineAgents {
		b, err := bs.GetBudget(ctx, agent)
		if err != nil {
			continue // no record yet — 0 spend
		}
		total += b.SpentMonthlyCents
	}
	return total
}

// checkBudgetGate returns nil if the agent is allowed to make a Claude API call.
// Returns a non-nil error describing the reason if the budget is exceeded.
// At 80% it logs a warning but still allows the call.
// At 95% it blocks the call.
func checkBudgetGate(ctx context.Context, bs *budget.BudgetStore, agentName string) error {
	if bs == nil {
		return nil // no budget store — allow
	}

	cap := monthlyCapCents()
	spent := totalPipelineSpend(ctx, bs)

	pct := (spent * 100) / cap

	if pct >= pauseThresholdPct {
		return fmt.Errorf("budget gate: pipeline spend %d/%d cents (%d%%) exceeds %d%% threshold — pausing %s",
			spent, cap, pct, pauseThresholdPct, agentName)
	}

	if pct >= warnThresholdPct {
		fmt.Fprintf(os.Stderr, "[octi-pulpo] budget warning: pipeline spend %d/%d cents (%d%%) — %s proceeding (Haiku only)\n",
			spent, cap, pct, agentName)
	}

	return nil
}

// recordBudgetCost records a completed API call's cost in the budget store.
// Errors are logged to stderr but not propagated — budget tracking is best-effort.
func recordBudgetCost(ctx context.Context, bs *budget.BudgetStore, agentName string, costCents, tokensIn, tokensOut int) {
	if bs == nil || costCents <= 0 {
		return
	}
	if err := bs.RecordCost(ctx, agentName, costCents, tokensIn, tokensOut); err != nil {
		fmt.Fprintf(os.Stderr, "[octi-pulpo] budget record error for %s: %v\n", agentName, err)
	}
}
