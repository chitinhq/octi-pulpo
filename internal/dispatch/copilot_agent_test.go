package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// loadFixture unmarshals a testdata JSON payload into the generic
// map shape the webhook handler receives.
func loadFixture(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return out
}

func TestDetectCopilotAgentEvent_IssueAssigned(t *testing.T) {
	payload := loadFixture(t, "copilot_issue_assigned.json")
	ev := DetectCopilotAgentEvent("issues", "assigned", payload)
	if ev == nil {
		t.Fatal("expected a dispatched event, got nil")
	}
	if ev.Kind != CopilotAgentDispatched {
		t.Errorf("kind = %q, want dispatched", ev.Kind)
	}
	if ev.Org != "agentguardhq" {
		t.Errorf("org = %q, want agentguardhq", ev.Org)
	}
	if ev.Repo != "agentguardhq/widget" {
		t.Errorf("repo = %q", ev.Repo)
	}
	if ev.Issue != 42 {
		t.Errorf("issue = %d, want 42", ev.Issue)
	}
}

func TestDetectCopilotAgentEvent_IssueUnassigned(t *testing.T) {
	payload := loadFixture(t, "copilot_issue_assigned.json")
	ev := DetectCopilotAgentEvent("issues", "unassigned", payload)
	if ev == nil || ev.Kind != CopilotAgentCanceled {
		t.Fatalf("expected canceled, got %+v", ev)
	}
}

func TestDetectCopilotAgentEvent_PROpenedByBot(t *testing.T) {
	payload := loadFixture(t, "copilot_pr_opened.json")
	ev := DetectCopilotAgentEvent("pull_request", "opened", payload)
	if ev == nil {
		t.Fatal("expected completed event, got nil")
	}
	if ev.Kind != CopilotAgentCompleted {
		t.Errorf("kind = %q, want completed", ev.Kind)
	}
	if ev.PR != 137 {
		t.Errorf("pr = %d, want 137", ev.PR)
	}
	if ev.Issue != 42 {
		t.Errorf("linked issue = %d, want 42 (from 'Fixes #42' in body)", ev.Issue)
	}
	if ev.Actor != CopilotAgentBot {
		t.Errorf("actor = %q, want %q", ev.Actor, CopilotAgentBot)
	}
}

func TestDetectCopilotAgentEvent_IgnoresHumanPR(t *testing.T) {
	payload := map[string]interface{}{
		"repository": map[string]interface{}{
			"full_name": "chitinhq/octi",
			"owner":     map[string]interface{}{"login": "chitinhq"},
		},
		"pull_request": map[string]interface{}{
			"number": float64(1),
			"user":   map[string]interface{}{"login": "jaredpleva"},
			"body":   "Fixes #1",
		},
	}
	if ev := DetectCopilotAgentEvent("pull_request", "opened", payload); ev != nil {
		t.Fatalf("expected nil for human-authored PR, got %+v", ev)
	}
}

func TestDetectCopilotAgentEvent_IgnoresNonAssigneeLabeled(t *testing.T) {
	payload := map[string]interface{}{
		"repository": map[string]interface{}{
			"full_name": "agentguardhq/widget",
			"owner":     map[string]interface{}{"login": "agentguardhq"},
		},
		"issue": map[string]interface{}{"number": float64(7)},
		"assignee": map[string]interface{}{"login": "some-human"},
	}
	if ev := DetectCopilotAgentEvent("issues", "assigned", payload); ev != nil {
		t.Fatalf("expected nil for non-Copilot assignee, got %+v", ev)
	}
}

func TestCopilotAgentEvent_ToDispatchRecord_Tier(t *testing.T) {
	ev := &CopilotAgentEvent{
		Kind:  CopilotAgentDispatched,
		Org:   "agentguardhq",
		Repo:  "agentguardhq/widget",
		Issue: 42,
	}
	now, _ := time.Parse(time.RFC3339, "2026-04-15T19:30:00Z")
	rec := ev.ToDispatchRecord("did_abc", now)
	if rec.Driver != CopilotAgentDriver {
		t.Errorf("driver = %q", rec.Driver)
	}
	if rec.Tier != "copilot" {
		t.Errorf("tier = %q, want copilot", rec.Tier)
	}
	if rec.Result != "dispatched" {
		t.Errorf("result = %q", rec.Result)
	}
	if rec.DispatchID != "did_abc" {
		t.Errorf("dispatch_id = %q", rec.DispatchID)
	}
	if rec.Event.Payload["issue"] != "42" {
		t.Errorf("payload.issue = %q", rec.Event.Payload["issue"])
	}
	// Sanity: ClassifyTier on the persisted driver also gives "copilot".
	if got := ClassifyTier(rec.Driver, rec.Event); got != "copilot" {
		t.Errorf("ClassifyTier = %q, want copilot", got)
	}
}

