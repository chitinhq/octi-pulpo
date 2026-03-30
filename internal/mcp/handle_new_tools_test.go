package mcp

import (
	"strings"
	"testing"
)

// --- Nil-store guard tests for tools added after PR #77 ---

func TestHandleToolCall_StandupReport_NilStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "standup_report",
		"arguments": map[string]interface{}{
			"squad": "octi-pulpo",
			"date":  "2026-03-30",
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when standup store is nil")
	}
	if !strings.Contains(resp.Error.Message, "standup store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_StandupRead_NilStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "standup_read",
		"arguments": map[string]interface{}{"squad": "octi-pulpo"},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when standup store is nil")
	}
	if !strings.Contains(resp.Error.Message, "standup store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_OrgChart_NilStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "org_chart",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when org store is nil")
	}
	if !strings.Contains(resp.Error.Message, "org store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_BudgetStatus_NilStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "budget_status",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when budget store is nil")
	}
	if !strings.Contains(resp.Error.Message, "budget store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_BudgetSet_NilStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "budget_set",
		"arguments": map[string]interface{}{
			"agent":                "test-agent",
			"budget_monthly_cents": 5000,
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when budget store is nil")
	}
	if !strings.Contains(resp.Error.Message, "budget store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_BudgetReset_NilStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "budget_reset",
		"arguments": map[string]interface{}{"agent": "test-agent"},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when budget store is nil")
	}
	if !strings.Contains(resp.Error.Message, "budget store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_SprintCreate_NilStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "sprint_create",
		"arguments": map[string]interface{}{
			"title": "Test task",
			"squad": "octi-pulpo",
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when sprint store is nil")
	}
	if !strings.Contains(resp.Error.Message, "sprint store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_SprintReprioritize_NilStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "sprint_reprioritize",
		"arguments": map[string]interface{}{
			"issue_num": 42,
			"priority":  1,
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when sprint store is nil")
	}
	if !strings.Contains(resp.Error.Message, "sprint store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_SprintComplete_NilStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "sprint_complete",
		"arguments": map[string]interface{}{
			"issue_num": 42,
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when sprint store is nil")
	}
	if !strings.Contains(resp.Error.Message, "sprint store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

// --- Input validation guard tests (no store required) ---

// sprint_reprioritize and sprint_complete guard nil sprintStore before validating
// issue_num, so the nil-store error fires first when both conditions are true.
func TestHandleToolCall_SprintReprioritize_MissingIssueNum(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "sprint_reprioritize",
		"arguments": map[string]interface{}{
			"priority": 1,
			// issue_num omitted (zero value)
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error (nil sprint store fires before issue_num check)")
	}
	// nil store guard fires first
	if !strings.Contains(resp.Error.Message, "sprint store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_SprintComplete_MissingIssueNum(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "sprint_complete",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error (nil sprint store fires before issue_num check)")
	}
	// nil store guard fires first
	if !strings.Contains(resp.Error.Message, "sprint store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_BudgetSet_MissingAgent(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "budget_set",
		"arguments": map[string]interface{}{
			"budget_monthly_cents": 5000,
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for missing agent")
	}
	// nil budgetStore guard fires before agent validation
	if resp.Error.Code != -32000 {
		t.Errorf("error code: got %d, want -32000 (nil store)", resp.Error.Code)
	}
}

func TestHandleToolCall_BudgetReset_MissingAgent(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "budget_reset",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for missing agent")
	}
	// nil budgetStore guard fires before agent validation
	if resp.Error.Code != -32000 {
		t.Errorf("error code: got %d, want -32000 (nil store)", resp.Error.Code)
	}
}

func TestHandleToolCall_RequestWork_MissingFields(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "request_work",
		"arguments": map[string]interface{}{
			"to_squad": "kernel",
			// description missing
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for missing description")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
}

func TestHandleToolCall_RequestWork_MissingSquad(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "request_work",
		"arguments": map[string]interface{}{
			"description": "need review",
			// to_squad missing
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for missing to_squad")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
}

func TestHandleToolCall_CheckRequests_MissingSquad(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "check_requests",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for missing squad")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "squad") {
		t.Errorf("error message should mention squad, got %q", resp.Error.Message)
	}
}

func TestHandleToolCall_FulfillRequest_MissingFields(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "fulfill_request",
		"arguments": map[string]interface{}{
			"request_id": "req-123",
			// result missing
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for missing result")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
}

func TestHandleToolCall_CircuitReset_MissingDriver(t *testing.T) {
	s := newTestServer(t)
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "circuit_reset",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for missing driver")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "driver") {
		t.Errorf("error message should mention driver, got %q", resp.Error.Message)
	}
}

func TestHandleToolCall_StandupRead_MissingSquad(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "standup_read",
		"arguments": map[string]interface{}{"all": false},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when standup store is nil")
	}
	// nil standupStore guard fires before squad validation
	if !strings.Contains(resp.Error.Message, "standup store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_DispatchTrigger_MissingAgent(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "dispatch_trigger",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when dispatcher is nil")
	}
	if !strings.Contains(resp.Error.Message, "dispatcher not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}
