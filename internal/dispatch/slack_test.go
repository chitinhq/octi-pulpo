package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
)

func TestNotifier_Enabled(t *testing.T) {
	if NewNotifier("").Enabled() {
		t.Fatal("empty URL should not be enabled")
	}
	if !NewNotifier("http://example.com/hook").Enabled() {
		t.Fatal("non-empty URL should be enabled")
	}
}

func TestNotifier_NoopWhenDisabled(t *testing.T) {
	ctx := context.Background()
	n := NewNotifier("")

	// All Post* calls must return nil without making any HTTP requests.
	if err := n.PostBudgetDashboard(ctx, nil, 0, 0); err != nil {
		t.Fatalf("PostBudgetDashboard: %v", err)
	}
	if err := n.PostDriversDown(ctx, "desc"); err != nil {
		t.Fatalf("PostDriversDown: %v", err)
	}
	if err := n.PostDriversRecovered(ctx); err != nil {
		t.Fatalf("PostDriversRecovered: %v", err)
	}
}

func TestNotifier_PostBudgetDashboard(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	drivers := []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "CLOSED", Failures: 0},
		{Name: "copilot", CircuitState: "OPEN", Failures: 12},
	}

	if err := n.PostBudgetDashboard(ctx, drivers, 80, 20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	text, _ := payload["text"].(string)
	if !strings.Contains(text, "claude-code") {
		t.Error("expected claude-code in dashboard text")
	}
	if !strings.Contains(text, "copilot") {
		t.Error("expected copilot in dashboard text")
	}
	if !strings.Contains(text, "80.0%") {
		t.Errorf("expected 80.0%% pass rate, got: %s", text)
	}
}

func TestNotifier_PostDriversDown(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostDriversDown(ctx, "all circuit breakers OPEN"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	text, _ := payload["text"].(string)
	if !strings.Contains(text, "All Drivers Exhausted") {
		t.Errorf("expected 'All Drivers Exhausted' in text, got: %s", text)
	}
	if !strings.Contains(text, "all circuit breakers OPEN") {
		t.Errorf("expected description in text, got: %s", text)
	}
}

func TestNotifier_PostDriversRecovered(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostDriversRecovered(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	text, _ := payload["text"].(string)
	if !strings.Contains(text, "Drivers Recovered") {
		t.Errorf("expected 'Drivers Recovered' in text, got: %s", text)
	}
}

func TestNotifier_WebhookError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	err := n.PostDriversRecovered(ctx)
	if err == nil {
		t.Fatal("expected error on non-200 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error, got: %v", err)
	}
}

func TestNotifier_PostSprintDigest_NoopWhenDisabled(t *testing.T) {
	ctx := context.Background()
	n := NewNotifier("")
	err := n.PostSprintDigest(ctx, nil, 0, 0, nil)
	if err != nil {
		t.Fatalf("PostSprintDigest on disabled notifier: %v", err)
	}
}

func TestNotifier_PostSprintDigest_Basic(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	drivers := []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "CLOSED", Failures: 0},
		{Name: "codex", CircuitState: "OPEN", Failures: 73},
	}
	items := []sprint.SprintItem{
		{IssueNum: 1, Repo: "AgentGuardHQ/octi-pulpo", Title: "feat A", Status: "done", Priority: 0},
		{IssueNum: 2, Repo: "AgentGuardHQ/octi-pulpo", Title: "feat B", Status: "open", Priority: 1},
		{IssueNum: 3, Repo: "AgentGuardHQ/octi-pulpo", Title: "feat C", Status: "pr_open", PRNumber: 42, Priority: 1},
	}

	if err := n.PostSprintDigest(ctx, drivers, 90, 10, items); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}
	text, _ := payload["text"].(string)

	checks := []struct {
		want string
		desc string
	}{
		{"Sprint Digest", "header"},
		{"90.0%", "pass rate"},
		{"claude-code", "driver name"},
		{"codex", "driver name"},
		{"73 failures", "failure count"},
		{"Done: 1", "done count"},
		{"PR Open: 1", "pr_open count"},
		{"Open: 1", "open count"},
		{"feat C", "PR item title"},
		{"#42", "PR number"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("expected %q (%s) in digest text, got:\n%s", c.want, c.desc, text)
		}
	}
}

func TestNotifier_PostSprintDigest_ShowsBlockers(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	// Item 20 depends on item 10, which is still open → blocker
	items := []sprint.SprintItem{
		{IssueNum: 10, Repo: "AgentGuardHQ/octi-pulpo", Title: "dep item", Status: "open", Priority: 1},
		{IssueNum: 20, Repo: "AgentGuardHQ/octi-pulpo", Title: "blocked item", Status: "open", Priority: 2, DependsOn: []int{10}},
	}

	if err := n.PostSprintDigest(ctx, nil, 0, 0, items); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	text, _ := payload["text"].(string)

	if !strings.Contains(text, "Blockers") {
		t.Errorf("expected 'Blockers' section in digest, got:\n%s", text)
	}
	if !strings.Contains(text, "blocked item") {
		t.Errorf("expected blocked item title in digest, got:\n%s", text)
	}
	if !strings.Contains(text, "#10") {
		t.Errorf("expected dependency #10 listed, got:\n%s", text)
	}
}

func TestNotifier_PostSprintDigest_NoBlockersWhenDepDone(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	// Item 20 depends on item 10, which is done → not a blocker
	items := []sprint.SprintItem{
		{IssueNum: 10, Repo: "AgentGuardHQ/octi-pulpo", Title: "dep item", Status: "done", Priority: 0},
		{IssueNum: 20, Repo: "AgentGuardHQ/octi-pulpo", Title: "unblocked item", Status: "open", Priority: 1, DependsOn: []int{10}},
	}

	if err := n.PostSprintDigest(ctx, nil, 0, 0, items); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	text, _ := payload["text"].(string)
	if strings.Contains(text, "Blockers") {
		t.Errorf("expected no 'Blockers' section when dep is done, got:\n%s", text)
	}
}

func TestNotifier_PostSprintDigest_EmptyItems(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	drivers := []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "CLOSED"},
	}
	if err := n.PostSprintDigest(ctx, drivers, 50, 50, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "Sprint Digest") {
		t.Errorf("expected Sprint Digest header with no items, got:\n%s", text)
	}
	if strings.Contains(text, "Sprint:") {
		t.Errorf("expected no Sprint: line when items is nil, got:\n%s", text)
	}
}

func TestBrain_SetNotifier(t *testing.T) {
	d, _ := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	n := NewNotifier("") // disabled
	brain.SetNotifier(n)

	if brain.notifier != n {
		t.Fatal("SetNotifier did not set the notifier")
	}
}

func TestBrain_MaybePostDashboard_NoopWhenDisabled(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())
	brain.SetNotifier(NewNotifier("")) // disabled

	// Should not panic or error even with no-op notifier
	brain.maybePostDashboard(ctx)
}

func TestBrain_MaybeNotifyConstraintChange_EdgeTriggered(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())
	brain.SetNotifier(NewNotifier(srv.URL))

	downConstraint := Constraint{Type: "all_drivers_down", Description: "all down", Severity: 0}
	noneConstraint := Constraint{Type: "none", Description: "healthy", Severity: 2}

	// First down transition: should fire PostDriversDown
	brain.maybeNotifyConstraintChange(ctx, downConstraint)
	if callCount != 1 {
		t.Fatalf("expected 1 Slack call on first down transition, got %d", callCount)
	}

	// Still down: should NOT fire again (edge-triggered)
	brain.maybeNotifyConstraintChange(ctx, downConstraint)
	if callCount != 1 {
		t.Fatalf("expected no additional Slack call when still down, got %d", callCount)
	}

	// Recovery transition: should fire PostDriversRecovered
	brain.maybeNotifyConstraintChange(ctx, noneConstraint)
	if callCount != 2 {
		t.Fatalf("expected 1 additional Slack call on recovery, got %d", callCount)
	}

	// Still healthy: no additional calls
	brain.maybeNotifyConstraintChange(ctx, noneConstraint)
	if callCount != 2 {
		t.Fatalf("expected no additional Slack call when still healthy, got %d", callCount)
	}
}
