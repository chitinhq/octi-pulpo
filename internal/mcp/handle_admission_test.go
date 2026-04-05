package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chitinhq/octi-pulpo/internal/admission"
)

// newTestServerWithAdmission wires a real admission.Gate (nil Redis — Score is
// pure logic and does not touch Redis).
func newTestServerWithAdmission(t *testing.T) *Server {
	t.Helper()
	s := newTestServerNoRouter()
	s.SetAdmissionGate(admission.New(nil, "test"))
	return s
}

// --- Nil-guard tests for admission tools added in PR #99 ---

func TestHandleToolCall_AdmitTask_NilGate(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "admit_task",
		"arguments": map[string]interface{}{"title": "Deploy feature", "squad": "kernel"},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when admission gate is nil")
	}
	if !strings.Contains(resp.Error.Message, "admission gate not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_LockDomain_NilGate(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "lock_domain",
		"arguments": map[string]interface{}{"domain": "branch:feat/auth", "holder": "agent-1"},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when admission gate is nil")
	}
	if !strings.Contains(resp.Error.Message, "admission gate not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_UnlockDomain_NilGate(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "unlock_domain",
		"arguments": map[string]interface{}{"domain": "branch:feat/auth", "holder": "agent-1"},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when admission gate is nil")
	}
	if !strings.Contains(resp.Error.Message, "admission gate not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_ListDomainLocks_NilGate(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "list_domain_locks",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when admission gate is nil")
	}
	if !strings.Contains(resp.Error.Message, "admission gate not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

// --- Invalid argument tests ---

func TestHandleToolCall_LockDomain_InvalidJSON(t *testing.T) {
	s := newTestServerWithAdmission(t)
	req := Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"lock_domain","arguments":{not valid json`),
	}
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for invalid JSON params")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
}

func TestHandleToolCall_UnlockDomain_InvalidJSON(t *testing.T) {
	s := newTestServerWithAdmission(t)
	req := Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"unlock_domain","arguments":{bad json`),
	}
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for invalid JSON params")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
}

// --- admit_task: Score logic (no Redis required) ---

func TestHandleToolCall_AdmitTask_Accept(t *testing.T) {
	s := newTestServerWithAdmission(t)
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "admit_task",
		"arguments": map[string]interface{}{
			"title":        "Add unit test",
			"squad":        "octi-pulpo",
			"spec_clarity": 0.9,
			"is_reversible": true,
			"priority":     1,
		},
	})
	resp := s.handle(req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	content, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("result should be a map")
	}
	items, ok := content["content"].([]map[string]string)
	if !ok || len(items) == 0 {
		t.Fatal("expected content items in result")
	}
	var score admission.IntakeScore
	if err := json.Unmarshal([]byte(items[0]["text"]), &score); err != nil {
		t.Fatalf("admit_task result is not valid IntakeScore JSON: %v\ntext: %s", err, items[0]["text"])
	}
	if score.Verdict != admission.VerdictAccept {
		t.Errorf("verdict: got %q, want ACCEPT", score.Verdict)
	}
	if score.Score < 0.85 {
		t.Errorf("score: got %.2f, want >= 0.85", score.Score)
	}
}

func TestHandleToolCall_AdmitTask_LowClarity_RoutesToPreflight(t *testing.T) {
	s := newTestServerWithAdmission(t)
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "admit_task",
		"arguments": map[string]interface{}{
			"title":        "Do the thing",
			"squad":        "kernel",
			"spec_clarity": 0.2, // below 0.5 threshold
		},
	})
	resp := s.handle(req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	content, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("result should be a map")
	}
	items, ok := content["content"].([]map[string]string)
	if !ok || len(items) == 0 {
		t.Fatal("expected content items in result")
	}
	var score admission.IntakeScore
	if err := json.Unmarshal([]byte(items[0]["text"]), &score); err != nil {
		t.Fatalf("admit_task result is not valid IntakeScore JSON: %v\ntext: %s", err, items[0]["text"])
	}
	if score.Verdict != admission.VerdictPreflight {
		t.Errorf("verdict: got %q, want ROUTE_TO_PREFLIGHT", score.Verdict)
	}
}

func TestHandleToolCall_AdmitTask_HighBlastRadius_Deferred(t *testing.T) {
	s := newTestServerWithAdmission(t)
	// 15 files → blast radius > 10, penalty -0.20, score = 0.80 → DEFER
	files := make([]interface{}, 15)
	for i := range files {
		files[i] = "file.go"
	}
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "admit_task",
		"arguments": map[string]interface{}{
			"title":        "Refactor 15 files",
			"squad":        "kernel",
			"spec_clarity": 0.8,
			"file_paths":   files,
		},
	})
	resp := s.handle(req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	content, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("result should be a map")
	}
	items, ok := content["content"].([]map[string]string)
	if !ok || len(items) == 0 {
		t.Fatal("expected content items in result")
	}
	var score admission.IntakeScore
	if err := json.Unmarshal([]byte(items[0]["text"]), &score); err != nil {
		t.Fatalf("admit_task result is not valid IntakeScore JSON: %v\ntext: %s", err, items[0]["text"])
	}
	if score.Verdict != admission.VerdictDefer {
		t.Errorf("verdict: got %q, want DEFER (15 files, score 0.80)", score.Verdict)
	}
	if score.BlastRadius != 15 {
		t.Errorf("blast_radius: got %d, want 15", score.BlastRadius)
	}
}

// --- SetAdmissionGate wires the gate correctly ---

func TestSetAdmissionGate_NilThenSet(t *testing.T) {
	s := newTestServerNoRouter()

	// Before: gate is nil, expect error.
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "admit_task",
		"arguments": map[string]interface{}{"title": "x", "spec_clarity": 0.9},
	})
	resp := s.handle(req)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "admission gate not initialized") {
		t.Fatalf("expected nil-gate error before SetAdmissionGate, got: %v", resp.Error)
	}

	// After: gate is set, expect a successful score.
	s.SetAdmissionGate(admission.New(nil, "test"))
	resp = s.handle(req)
	if resp.Error != nil {
		t.Errorf("expected success after SetAdmissionGate, got error: %v", resp.Error)
	}
}
