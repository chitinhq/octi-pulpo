package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIsCreditExhaustion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "claude credit balance message",
			output: "Error: Credit balance is too low. Please purchase more credits.",
			want:   true,
		},
		{
			name:   "usage limit hit",
			output: "You have hit your usage limit for this period.",
			want:   true,
		},
		{
			name:   "quota exhausted sentinel",
			output: "reason=QUOTA_EXHAUSTED exiting",
			want:   true,
		},
		{
			name:   "capacity exhausted",
			output: "You have exhausted your capacity for this billing cycle.",
			want:   true,
		},
		{
			name:   "run-agent.sh budget_exhausted label",
			output: "[2026-03-29T12:00:00Z] FAILURE_REASON=budget_exhausted driver=claude-code",
			want:   true,
		},
		{
			name:   "all drivers at budget cap",
			output: "SKIP: octi-pulpo-sr — all drivers at budget cap, no healthy fallback",
			want:   true,
		},
		{
			name:   "mixed case",
			output: "CREDIT BALANCE IS TOO LOW",
			want:   true,
		},
		{
			name:   "normal failure — not credit",
			output: "fatal: not a git repository (or any of the parent directories): .git",
			want:   false,
		},
		{
			name:   "timeout — not credit",
			output: "Timeout: agent exceeded 300s wall time",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
		{
			name:   "429 not included (rate limit vs budget are separate)",
			output: "HTTP 429 Too Many Requests — back off and retry",
			want:   false, // 429 is a transient rate limit, not budget exhaustion
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCreditExhaustion(tt.output); got != tt.want {
				t.Errorf("isCreditExhaustion(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestAgentDriver_ReadsSchedule(t *testing.T) {
	type agentCfg struct {
		Driver string `json:"driver"`
	}
	sched := map[string]interface{}{
		"agents": map[string]agentCfg{
			"kernel-sr":    {Driver: "claude-code"},
			"kernel-qa":    {Driver: "copilot"},
			"codex-worker": {Driver: "codex"},
		},
	}

	data, _ := json.Marshal(sched)
	f := filepath.Join(t.TempDir(), "schedule.json")
	os.WriteFile(f, data, 0644)

	tests := []struct {
		agent string
		want  string
	}{
		{"kernel-sr", "claude-code"},
		{"kernel-qa", "copilot"},
		{"codex-worker", "codex"},
		{"unknown-agent", "claude-code"}, // default
	}

	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			got := agentDriver(f, tt.agent)
			if got != tt.want {
				t.Errorf("agentDriver(%q) = %q, want %q", tt.agent, got, tt.want)
			}
		})
	}
}

func TestAgentDriver_MissingFile(t *testing.T) {
	got := agentDriver("/nonexistent/schedule.json", "any-agent")
	if got != "claude-code" {
		t.Errorf("agentDriver with missing file = %q, want claude-code", got)
	}
}

func TestAgentDriver_MalformedJSON(t *testing.T) {
	f := filepath.Join(t.TempDir(), "schedule.json")
	os.WriteFile(f, []byte("{not valid json"), 0644)

	got := agentDriver(f, "any-agent")
	if got != "claude-code" {
		t.Errorf("agentDriver with bad JSON = %q, want claude-code", got)
	}
}

func TestCappedBuffer_CapsAtMaxSize(t *testing.T) {
	var buf cappedBuffer
	buf.maxSize = 10

	input := []byte("hello world this is too long")
	n, err := buf.Write(input)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// cappedBuffer always returns len(p) to satisfy io.Writer contract used by
	// io.MultiWriter — short writes would cause MultiWriter to abort.
	if n != len(input) {
		t.Errorf("Write returned %d, want %d (full len(p))", n, len(input))
	}
	got := buf.String()
	if len(got) > 10 {
		t.Errorf("buffer length = %d, want <= 10; got %q", len(got), got)
	}
	if got != "hello worl" {
		t.Errorf("buffer contents = %q, want %q", got, "hello worl")
	}
}

func TestCappedBuffer_AcceptsUpToMax(t *testing.T) {
	var buf cappedBuffer
	buf.maxSize = 5

	buf.Write([]byte("abc"))
	buf.Write([]byte("de")) // hits exactly max
	buf.Write([]byte("XY")) // should be dropped

	if got := buf.String(); got != "abcde" {
		t.Errorf("got %q, want %q", got, "abcde")
	}
}
