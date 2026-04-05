package dispatch

import (
	"testing"

	"github.com/chitinhq/octi-pulpo/internal/pipeline"
	"github.com/chitinhq/octi-pulpo/internal/routing"
)

func TestFormatPipelineDashboard(t *testing.T) {
	depths := map[pipeline.Stage]int{
		pipeline.StageArchitect:  1,
		pipeline.StageImplement:  5,
		pipeline.StageQA:         2,
		pipeline.StageReview:     3,
		pipeline.StageRelease:    0,
	}
	sessions := map[pipeline.Stage]int{
		pipeline.StageArchitect:  1,
		pipeline.StageImplement:  3,
		pipeline.StageQA:         1,
		pipeline.StageReview:     2,
	}
	budgets := []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "CLOSED", BudgetPct: intPtr(65)},
		{Name: "copilot", CircuitState: "CLOSED", BudgetPct: intPtr(80)},
	}

	blocks := FormatPipelineDashboard(depths, sessions, budgets, pipeline.BackpressureAction{})

	if len(blocks) == 0 {
		t.Fatal("expected non-empty blocks")
	}

	// Should contain stage names
	raw := blocksToString(blocks)
	if !containsString(raw, "ARCHITECT") {
		t.Error("expected ARCHITECT in dashboard")
	}
	if !containsString(raw, "IMPLEMENT") {
		t.Error("expected IMPLEMENT in dashboard")
	}
}

func intPtr(i int) *int { return &i }

func blocksToString(blocks []map[string]interface{}) string {
	s := ""
	for _, b := range blocks {
		if text, ok := b["text"].(map[string]interface{}); ok {
			if t, ok := text["text"].(string); ok {
				s += t + "\n"
			}
		}
	}
	return s
}

func containsString(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && contains(s, sub)
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestParsePipelineCommand(t *testing.T) {
	tests := []struct {
		input string
		cmd   PipelineCommand
		valid bool
	}{
		{"pipeline status", PipelineCommand{Action: "status"}, true},
		{"pipeline pause", PipelineCommand{Action: "pause"}, true},
		{"pipeline resume", PipelineCommand{Action: "resume"}, true},
		{"pipeline prioritize fix auth bug", PipelineCommand{Action: "prioritize", Args: "fix auth bug"}, true},
		{"hello", PipelineCommand{}, false},
		{"pipeline", PipelineCommand{Action: "status"}, true}, // bare "pipeline" = status
	}

	for _, tt := range tests {
		cmd, ok := ParsePipelineCommand(tt.input)
		if ok != tt.valid {
			t.Errorf("ParsePipelineCommand(%q) valid = %v, want %v", tt.input, ok, tt.valid)
		}
		if ok && cmd.Action != tt.cmd.Action {
			t.Errorf("ParsePipelineCommand(%q) action = %q, want %q", tt.input, cmd.Action, tt.cmd.Action)
		}
	}
}