package dispatch

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestLoopTelemetryFields verifies that LoopTelemetry carries all expected
// fields and that they round-trip through JSON correctly.
func TestLoopTelemetryFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	entry := LoopTelemetry{
		Timestamp:    now,
		TaskID:       "task-abc",
		Provider:     "deepseek",
		Model:        "deepseek-coder",
		Turn:         1,
		PromptTokens: 512,
		OutputTokens: 128,
		Cost:         0.0042,
		ToolCalls:    3,
		ToolErrors:   0,
		StopReason:   "end_turn",
		Duration:     2*time.Second + 500*time.Millisecond,
		Escalated:    false,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded LoopTelemetry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.TaskID != entry.TaskID {
		t.Errorf("TaskID: want %s, got %s", entry.TaskID, decoded.TaskID)
	}
	if decoded.Provider != entry.Provider {
		t.Errorf("Provider: want %s, got %s", entry.Provider, decoded.Provider)
	}
	if decoded.Model != entry.Model {
		t.Errorf("Model: want %s, got %s", entry.Model, decoded.Model)
	}
	if decoded.Turn != entry.Turn {
		t.Errorf("Turn: want %d, got %d", entry.Turn, decoded.Turn)
	}
	if decoded.PromptTokens != entry.PromptTokens {
		t.Errorf("PromptTokens: want %d, got %d", entry.PromptTokens, decoded.PromptTokens)
	}
	if decoded.OutputTokens != entry.OutputTokens {
		t.Errorf("OutputTokens: want %d, got %d", entry.OutputTokens, decoded.OutputTokens)
	}
	if decoded.ToolCalls != entry.ToolCalls {
		t.Errorf("ToolCalls: want %d, got %d", entry.ToolCalls, decoded.ToolCalls)
	}
	if decoded.StopReason != entry.StopReason {
		t.Errorf("StopReason: want %s, got %s", entry.StopReason, decoded.StopReason)
	}
	if decoded.Duration != entry.Duration {
		t.Errorf("Duration: want %v, got %v", entry.Duration, decoded.Duration)
	}
	if decoded.Escalated != entry.Escalated {
		t.Errorf("Escalated: want %v, got %v", entry.Escalated, decoded.Escalated)
	}
}

// TestTelemetryWriterWritesJSONL writes 2 entries and verifies exactly 2 valid
// JSONL lines are produced, each parseable as LoopTelemetry.
func TestTelemetryWriterWritesJSONL(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTelemetryWriter(&buf)

	entries := []LoopTelemetry{
		{
			Timestamp:  time.Now().UTC(),
			TaskID:     "task-1",
			Provider:   "deepseek",
			Model:      "deepseek-coder",
			Turn:       1,
			StopReason: "end_turn",
		},
		{
			Timestamp:  time.Now().UTC(),
			TaskID:     "task-2",
			Provider:   "anthropic",
			Model:      "claude-3-haiku-20241022",
			Turn:       2,
			Escalated:  true,
			StopReason: "max_tokens",
		},
	}

	for _, e := range entries {
		if err := tw.Write(e); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL lines, got %d", len(lines))
	}

	for i, line := range lines {
		var decoded LoopTelemetry
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Errorf("line %d: json.Unmarshal: %v", i, err)
			continue
		}
		if decoded.TaskID != entries[i].TaskID {
			t.Errorf("line %d TaskID: want %s, got %s", i, entries[i].TaskID, decoded.TaskID)
		}
		if decoded.Escalated != entries[i].Escalated {
			t.Errorf("line %d Escalated: want %v, got %v", i, entries[i].Escalated, decoded.Escalated)
		}
	}
}
