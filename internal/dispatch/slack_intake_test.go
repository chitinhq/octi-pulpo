package dispatch

import (
	"strings"
	"testing"
)

func TestClassifyMessage(t *testing.T) {
	tests := []struct {
		text string
		want MessageType
	}{
		{"pipeline status", MessageTypePipelineCmd},
		{"pipeline pause", MessageTypePipelineCmd},
		{"fix the auth bug in cloud", MessageTypeBrief},
		{"we need resume parsing for ReadyBench", MessageTypeBrief},
		{"hello", MessageTypeBrief},
	}

	for _, tt := range tests {
		got := ClassifyMessage(tt.text)
		if got != tt.want {
			t.Errorf("ClassifyMessage(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestFormatBriefIssue(t *testing.T) {
	title, body := FormatBriefIssue("fix the auth bug in cloud login", "U12345")

	if title == "" {
		t.Error("expected non-empty title")
	}
	if body == "" {
		t.Error("expected non-empty body")
	}
	if !strings.Contains(body, "U12345") {
		t.Error("expected user ID in body")
	}
	if !strings.Contains(body, "Slack brief") {
		t.Error("expected Slack brief marker in body")
	}
}
