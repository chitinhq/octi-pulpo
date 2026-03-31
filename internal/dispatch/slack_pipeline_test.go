package dispatch

import (
	"strings"
	"testing"

	"github.com/AgentGuardHQ/octi-pulpo/internal/pipeline"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

func TestFormatPipelineDashboard(t *testing.T) {
	depths := map[pipeline.Stage]int{
		pipeline.StageArchitect: 1, pipeline.StageImplement: 5,
		pipeline.StageQA: 2, pipeline.StageReview: 3, pipeline.StageRelease: 0,
	}
	sessions := map[pipeline.Stage]int{
		pipeline.StageArchitect: 1, pipeline.StageImplement: 3,
		pipeline.StageQA: 1, pipeline.StageReview: 2,
	}
	pct := 65
	budgets := []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "CLOSED", BudgetPct: &pct},
	}

	blocks := FormatPipelineDashboard(depths, sessions, budgets, pipeline.BackpressureAction{})

	if len(blocks) == 0 {
		t.Fatal("expected non-empty blocks")
	}
	raw := blocksToString(blocks)
	if !strings.Contains(raw, "ARCHITECT") {
		t.Error("expected ARCHITECT in dashboard")
	}
	if !strings.Contains(raw, "IMPLEMENT") {
		t.Error("expected IMPLEMENT in dashboard")
	}
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
		{"pipeline", PipelineCommand{Action: "status"}, true},
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

func TestFormatBudgetAlert(t *testing.T) {
	alert := FormatBudgetAlert("claude-code", 15, 2)
	if !strings.Contains(alert, "claude-code") {
		t.Error("expected driver name in alert")
	}
	if !strings.Contains(alert, "15%") {
		t.Error("expected percentage in alert")
	}
	if !strings.Contains(alert, "2 architect") {
		t.Error("expected queued task count in alert")
	}
}

func TestFormatEscalation(t *testing.T) {
	blocks := FormatEscalation("AgentGuardHQ/agentguard", 42, "High blast radius: modifies auth middleware", 55)
	if len(blocks) == 0 {
		t.Fatal("expected non-empty blocks")
	}
	raw := blocksToString(blocks)
	if !strings.Contains(raw, "#42") {
		t.Error("expected PR number in escalation")
	}
}

func blocksToString(blocks []map[string]interface{}) string {
	var sb strings.Builder
	for _, b := range blocks {
		switch text := b["text"].(type) {
		case map[string]interface{}:
			if t, ok := text["text"].(string); ok {
				sb.WriteString(t)
				sb.WriteByte('\n')
			}
		case map[string]string:
			if t, ok := text["text"]; ok {
				sb.WriteString(t)
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}
