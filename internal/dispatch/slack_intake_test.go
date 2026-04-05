package dispatch

import "testing"

func TestClassifyMessage(t *testing.T) {
	tests := []struct {
		text string
		want MessageType
	}{
		{"pipeline status", MessageTypePipelineCmd},
		{"pipeline pause", MessageTypePipelineCmd},
		{"fix the auth bug in cloud", MessageTypeBrief},
		{"we need resume parsing for ReadyBench", MessageTypeBrief},
		{"hello", MessageTypeBrief}, // anything non-pipeline is a brief
	}

	for _, tt := range tests {
		got := ClassifyMessage(tt.text)
		if got != tt.want {
			t.Errorf("ClassifyMessage(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestFormatBudgetAlert(t *testing.T) {
	alert := FormatBudgetAlert("claude-code", 15, 2)

	if !contains(alert, "claude-code") {
		t.Error("expected driver name in alert")
	}
	if !contains(alert, "15%") {
		t.Error("expected percentage in alert")
	}
	if !contains(alert, "2 architect") {
		t.Error("expected queued task count in alert")
	}
}

func TestFormatEscalation(t *testing.T) {
	blocks := FormatEscalation("chitinhq/agentguard", 42, "High blast radius: modifies auth middleware", 55)

	if len(blocks) == 0 {
		t.Fatal("expected non-empty blocks")
	}
	raw := blocksToString(blocks)
	if !contains(raw, "#42") {
		t.Error("expected PR number in escalation")
	}
	if !contains(raw, "auth middleware") {
		t.Error("expected reason in escalation")
	}
}