package mcp

import (
	"strings"
	"testing"

	"github.com/chitinhq/octi-pulpo/internal/admission"
	"github.com/chitinhq/octi-pulpo/internal/budget"
	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/chitinhq/octi-pulpo/internal/org"
	"github.com/chitinhq/octi-pulpo/internal/sprint"
	"github.com/chitinhq/octi-pulpo/internal/standup"
	"github.com/redis/go-redis/v9"
)

// ─── Set* setter tests ────────────────────────────────────────────────────────
// All test files use package mcp (not mcp_test), so unexported struct fields
// are accessible directly. These tests verify each setter writes the field.

func TestServer_SetDispatcher(t *testing.T) {
	s := &Server{}
	d := new(dispatch.Dispatcher)
	s.SetDispatcher(d)
	if s.dispatcher != d {
		t.Error("SetDispatcher did not assign dispatcher field")
	}
}

func TestServer_SetSprintStore(t *testing.T) {
	s := &Server{}
	ss := new(sprint.Store)
	s.SetSprintStore(ss)
	if s.sprintStore != ss {
		t.Error("SetSprintStore did not assign sprintStore field")
	}
}

func TestServer_SetBenchmark(t *testing.T) {
	s := &Server{}
	bt := new(dispatch.BenchmarkTracker)
	s.SetBenchmark(bt)
	if s.benchmark != bt {
		t.Error("SetBenchmark did not assign benchmark field")
	}
}

func TestServer_SetStandupStore(t *testing.T) {
	s := &Server{}
	ss := new(standup.Store)
	s.SetStandupStore(ss)
	if s.standupStore != ss {
		t.Error("SetStandupStore did not assign standupStore field")
	}
}

func TestServer_SetProfileStore(t *testing.T) {
	s := &Server{}
	ps := new(dispatch.ProfileStore)
	s.SetProfileStore(ps)
	if s.profiles != ps {
		t.Error("SetProfileStore did not assign profiles field")
	}
}

func TestServer_SetRedis(t *testing.T) {
	s := &Server{}
	rdb := new(redis.Client)
	s.SetRedis(rdb, "test-ns")
	if s.rdb != rdb {
		t.Error("SetRedis did not assign rdb field")
	}
	if s.redisNS != "test-ns" {
		t.Errorf("SetRedis did not assign redisNS: got %q, want test-ns", s.redisNS)
	}
}

func TestServer_SetOrgStore(t *testing.T) {
	s := &Server{}
	o := new(org.OrgStore)
	s.SetOrgStore(o)
	if s.orgStore != o {
		t.Error("SetOrgStore did not assign orgStore field")
	}
}

func TestServer_SetBudgetStore(t *testing.T) {
	s := &Server{}
	b := new(budget.BudgetStore)
	s.SetBudgetStore(b)
	if s.budgetStore != b {
		t.Error("SetBudgetStore did not assign budgetStore field")
	}
}

func TestServer_SetGoalStore(t *testing.T) {
	s := &Server{}
	g := new(sprint.GoalStore)
	s.SetGoalStore(g)
	if s.goalStore != g {
		t.Error("SetGoalStore did not assign goalStore field")
	}
}

// SetAdmissionGate already has a test in handle_admission_test.go but that test
// uses a real Gate. Verify the basic field-assignment contract independently.
func TestServer_SetAdmissionGate_FieldAssignment(t *testing.T) {
	s := &Server{}
	if s.admissionGate != nil {
		t.Error("admissionGate should be nil on zero-value Server")
	}
	g := &admission.Gate{}
	s.SetAdmissionGate(g)
	if s.admissionGate != g {
		t.Error("SetAdmissionGate did not assign admissionGate field")
	}
}

// ─── Complementary input-validation tests ─────────────────────────────────────
// handle_new_tools_test.go covers most validation paths. These add the symmetric
// case: the OTHER required field is missing (request_id absent, result present).

func TestHandleToolCall_FulfillRequest_MissingRequestID(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "fulfill_request",
		"arguments": map[string]interface{}{
			"result": "task completed successfully",
			// request_id intentionally omitted
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error for missing request_id")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "request_id") {
		t.Errorf("error message should mention request_id, got %q", resp.Error.Message)
	}
}

// StandupRead with a wired standup store but squad="" and all=false reaches the
// squad-required validation path that the nil-store tests cannot exercise.
func TestHandleToolCall_StandupRead_WithStore_MissingSquad(t *testing.T) {
	s := newTestServerNoRouter()
	s.standupStore = new(standup.Store)

	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "standup_read",
		"arguments": map[string]interface{}{
			// squad omitted, all defaults to false
		},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when squad is empty and all=false")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "squad") {
		t.Errorf("error message should mention squad, got %q", resp.Error.Message)
	}
}
