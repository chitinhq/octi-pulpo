package flow

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSpan_HappyPath(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "events.jsonl")
	t.Setenv("MCPTRACE_FILE", dest)
	t.Setenv("CHITIN_AGENT_NAME", "test-agent")
	t.Setenv("CHITIN_SESSION_ID", "sess-xyz")

	func() {
		var err error
		defer Span("swarm.dispatch.anthropic", map[string]interface{}{"task_id": "T1"})(&err)
		time.Sleep(time.Millisecond)
	}()

	f, err := os.Open(dest)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var events []Event
	s := bufio.NewScanner(f)
	for s.Scan() {
		var ev Event
		if err := json.Unmarshal(s.Bytes(), &ev); err != nil {
			t.Fatalf("parse: %v", err)
		}
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events (started + completed), got %d", len(events))
	}
	if events[0].Action != "flow_started" || events[1].Action != "flow_completed" {
		t.Errorf("action sequence: %s / %s", events[0].Action, events[1].Action)
	}
	if events[0].Tool != "flow.swarm.dispatch.anthropic" {
		t.Errorf("tool: got %q", events[0].Tool)
	}
	if events[0].Source != "flow" {
		t.Errorf("source: got %q", events[0].Source)
	}
	if events[0].Agent != "test-agent" {
		t.Errorf("agent: got %q", events[0].Agent)
	}
	if events[0].SessionID != "sess-xyz" {
		t.Errorf("sid: got %q", events[0].SessionID)
	}
	if events[0].Outcome != "allow" || events[1].Outcome != "allow" {
		t.Errorf("outcomes: %s / %s", events[0].Outcome, events[1].Outcome)
	}
	if events[1].LatencyUs <= 0 {
		t.Errorf("completed latency not set: %d", events[1].LatencyUs)
	}
	if events[0].Fields["task_id"] != "T1" {
		t.Errorf("fields missing: %+v", events[0].Fields)
	}
}

func TestSpan_FailurePath(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "events.jsonl")
	t.Setenv("MCPTRACE_FILE", dest)

	boom := errors.New("boom")
	func() {
		var err error
		defer Span("swarm.triage", nil)(&err)
		err = boom
	}()

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var last Event
	s := bufio.NewScanner(bufio.NewReader(openFile(t, dest)))
	for s.Scan() {
		_ = json.Unmarshal(s.Bytes(), &last)
	}
	if last.Action != "flow_failed" {
		t.Errorf("expected flow_failed as last action, got %s (data=%s)", last.Action, data)
	}
	if last.Outcome != "deny" {
		t.Errorf("failed outcome: %s", last.Outcome)
	}
	if last.Fields == nil || last.Fields["error"] != "boom" {
		t.Errorf("error field missing: %+v", last.Fields)
	}
}

func TestAgentName_Default(t *testing.T) {
	t.Setenv("CHITIN_AGENT_NAME", "")
	if a := agentName(); a != "octi-pulpo" {
		t.Errorf("default agent: got %q", a)
	}
}

func openFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
