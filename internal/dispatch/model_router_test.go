package dispatch

import "testing"

func TestModelRouter_CopilotModels(t *testing.T) {
	r := NewModelRouter()
	tests := []struct {
		complexity string
		wantModel  string
	}{
		{"low", "gpt-5.4-nano"},
		{"med", "gpt-5.4-mini"},
		{"high", "gpt-5.4"},
	}
	for _, tt := range tests {
		got := r.CopilotModel(tt.complexity)
		if got != tt.wantModel {
			t.Errorf("CopilotModel(%q) = %q, want %q", tt.complexity, got, tt.wantModel)
		}
	}
}

func TestModelRouter_ClaudeModels(t *testing.T) {
	r := NewModelRouter()
	tests := []struct {
		complexity string
		wantModel  string
	}{
		{"low", "sonnet"},
		{"med", "sonnet"},
		{"high", "opus"},
	}
	for _, tt := range tests {
		got := r.ClaudeModel(tt.complexity)
		if got != tt.wantModel {
			t.Errorf("ClaudeModel(%q) = %q, want %q", tt.complexity, got, tt.wantModel)
		}
	}
}

func TestModelRouter_EscalationModel(t *testing.T) {
	r := NewModelRouter()
	got, ok := r.EscalateCopilot("gpt-5.4-nano")
	if !ok || got != "gpt-5.4-mini" {
		t.Errorf("EscalateCopilot(nano) = %q, %v", got, ok)
	}
	got, ok = r.EscalateCopilot("gpt-5.4-mini")
	if !ok || got != "gpt-5.4" {
		t.Errorf("EscalateCopilot(mini) = %q, %v", got, ok)
	}
	_, ok = r.EscalateCopilot("gpt-5.4")
	if ok {
		t.Error("EscalateCopilot(gpt-5.4) should return false — top of ladder")
	}
	got, ok = r.EscalateClaude("sonnet")
	if !ok || got != "opus" {
		t.Errorf("EscalateClaude(sonnet) = %q, %v", got, ok)
	}
	_, ok = r.EscalateClaude("opus")
	if ok {
		t.Error("EscalateClaude(opus) should return false — top of ladder")
	}
}

func TestModelRouter_DefaultsToLow(t *testing.T) {
	r := NewModelRouter()
	if got := r.CopilotModel("unknown"); got != "gpt-5.4-nano" {
		t.Errorf("CopilotModel(unknown) = %q, want gpt-5.4-nano", got)
	}
	if got := r.ClaudeModel(""); got != "sonnet" {
		t.Errorf("ClaudeModel('') = %q, want sonnet", got)
	}
}
