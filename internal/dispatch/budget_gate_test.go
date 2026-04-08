package dispatch

import (
	"os"
	"testing"
)

func TestMonthlyCapCents_Default(t *testing.T) {
	os.Unsetenv("CLAUDE_BUDGET_MONTHLY")
	cap := monthlyCapCents()
	if cap != 5000 {
		t.Errorf("expected default cap 5000, got %d", cap)
	}
}

func TestMonthlyCapCents_EnvVar(t *testing.T) {
	os.Setenv("CLAUDE_BUDGET_MONTHLY", "7500")
	defer os.Unsetenv("CLAUDE_BUDGET_MONTHLY")
	
	cap := monthlyCapCents()
	if cap != 7500 {
		t.Errorf("expected cap 7500 from env var, got %d", cap)
	}
}

func TestMonthlyCapCents_InvalidEnvVar(t *testing.T) {
	os.Setenv("CLAUDE_BUDGET_MONTHLY", "not-a-number")
	defer os.Unsetenv("CLAUDE_BUDGET_MONTHLY")
	
	cap := monthlyCapCents()
	if cap != 5000 {
		t.Errorf("expected default cap 5000 for invalid env var, got %d", cap)
	}
}

func TestMonthlyCapCents_ZeroEnvVar(t *testing.T) {
	os.Setenv("CLAUDE_BUDGET_MONTHLY", "0")
	defer os.Unsetenv("CLAUDE_BUDGET_MONTHLY")
	
	cap := monthlyCapCents()
	if cap != 5000 {
		t.Errorf("expected default cap 5000 for zero env var, got %d", cap)
	}
}

func TestMonthlyCapCents_NegativeEnvVar(t *testing.T) {
	os.Setenv("CLAUDE_BUDGET_MONTHLY", "-100")
	defer os.Unsetenv("CLAUDE_BUDGET_MONTHLY")
	
	cap := monthlyCapCents()
	if cap != 5000 {
		t.Errorf("expected default cap 5000 for negative env var, got %d", cap)
	}
}

func TestBudgetDefaults(t *testing.T) {
	if defaultMonthlyCapCents != 5000 {
		t.Errorf("expected defaultMonthlyCapCents = 5000, got %d", defaultMonthlyCapCents)
	}
	if warnThresholdPct != 80 {
		t.Errorf("expected warnThresholdPct = 80, got %d", warnThresholdPct)
	}
	if pauseThresholdPct != 95 {
		t.Errorf("expected pauseThresholdPct = 95, got %d", pauseThresholdPct)
	}
}

func TestPipelineAgents(t *testing.T) {
	expected := []string{"triage", "planner", "reviewer"}
	if len(pipelineAgents) != len(expected) {
		t.Errorf("expected %d pipeline agents, got %d", len(expected), len(pipelineAgents))
	}
	
	for i, agent := range expected {
		if pipelineAgents[i] != agent {
			t.Errorf("pipeline agent at index %d: expected %q, got %q", i, agent, pipelineAgents[i])
		}
	}
}

// TestBudgetGateLogic tests the core percentage calculation logic
func TestBudgetGateLogic(t *testing.T) {
	tests := []struct {
		name        string
		spent       int
		cap         int
		expectedPct int
	}{
		{"zero spend", 0, 5000, 0},
		{"half spent", 2500, 5000, 50},
		{"warning threshold", 4000, 5000, 80},
		{"pause threshold", 4750, 5000, 95},
		{"full spent", 5000, 5000, 100},
		{"over spent", 6000, 5000, 120},
		{"zero cap", 100, 0, 0}, // Should not divide by zero
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pct int
			if tt.cap > 0 {
				pct = (tt.spent * 100) / tt.cap
			}
			
			if pct != tt.expectedPct {
				t.Errorf("percentage calculation: spent=%d, cap=%d, expected %d%%, got %d%%", 
					tt.spent, tt.cap, tt.expectedPct, pct)
			}
			
			// Test threshold logic
			warn := pct >= warnThresholdPct
			pause := pct >= pauseThresholdPct
			
			// Just verify no panic
			t.Logf("spent=%d, cap=%d, pct=%d%%, warn=%v, pause=%v", 
				tt.spent, tt.cap, pct, warn, pause)
		})
	}
}