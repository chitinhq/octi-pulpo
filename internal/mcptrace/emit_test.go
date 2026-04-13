package mcptrace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmit_WritesJSONLToMCPTRACE_FILE(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "sub", "events.jsonl")
	t.Setenv("MCPTRACE_FILE", dest)

	Emit("octi", "agent-1", "sprint_status", "allow", "", time.Now().Add(-5*time.Millisecond))
	Emit("octi", "agent-1", "memory_store", "deny", "redis down", time.Now().Add(-2*time.Millisecond))

	f, err := os.Open(dest)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var events []Event
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("parse: %v", err)
		}
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].Tool != "mcp__octi__sprint_status" {
		t.Errorf("tool name: got %q", events[0].Tool)
	}
	if events[0].Action != "mcp_call" {
		t.Errorf("action: got %q", events[0].Action)
	}
	if events[0].Outcome != "allow" || events[1].Outcome != "deny" {
		t.Errorf("outcomes wrong: %s / %s", events[0].Outcome, events[1].Outcome)
	}
	if events[0].LatencyUs <= 0 || events[1].LatencyUs <= 0 {
		t.Errorf("latencies not set: %d / %d", events[0].LatencyUs, events[1].LatencyUs)
	}
	if events[0].Source != "octi" {
		t.Errorf("source: got %q", events[0].Source)
	}
}

func TestEmit_PrefersMCPTRACE_FILEOverWorkspace(t *testing.T) {
	dir := t.TempDir()
	wsPath := filepath.Join(dir, "ws")
	explicit := filepath.Join(dir, "explicit.jsonl")

	t.Setenv("CHITIN_WORKSPACE", wsPath)
	t.Setenv("MCPTRACE_FILE", explicit)

	Emit("atlas", "a", "wiki_read", "allow", "", time.Now())

	if _, err := os.Stat(explicit); err != nil {
		t.Errorf("MCPTRACE_FILE not used: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsPath, ".chitin", "events.jsonl")); err == nil {
		t.Error("workspace path should not be written when MCPTRACE_FILE is set")
	}
}

func TestEmit_FallsBackToWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCPTRACE_FILE", "")
	t.Setenv("CHITIN_WORKSPACE", dir)

	Emit("octi", "a", "ping", "allow", "", time.Now())

	data, err := os.ReadFile(filepath.Join(dir, ".chitin", "events.jsonl"))
	if err != nil {
		t.Fatalf("workspace fallback not written: %v", err)
	}
	if !strings.Contains(string(data), `"tool":"mcp__octi__ping"`) {
		t.Errorf("event not found: %s", data)
	}
}

func TestEmit_NoConfigIsNoop(t *testing.T) {
	t.Setenv("MCPTRACE_FILE", "")
	t.Setenv("CHITIN_WORKSPACE", "")
	t.Setenv("HOME", "")
	// Must not panic.
	Emit("octi", "a", "ping", "allow", "", time.Now())
}

func TestDestination(t *testing.T) {
	t.Setenv("MCPTRACE_FILE", "/tmp/x.jsonl")
	if got := destination(); got != "/tmp/x.jsonl" {
		t.Errorf("MCPTRACE_FILE precedence: got %q", got)
	}
}
