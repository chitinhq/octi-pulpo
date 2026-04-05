package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// --- Struct field tests ---

func TestTaskFields(t *testing.T) {
	task := Task{
		ID:       "t1",
		Type:     "code-gen",
		Repo:     "chitinhq/octi-pulpo",
		Prompt:   "write a hello world",
		Toolset:  []string{"read_file", "write_file"},
		Priority: "normal",
		Budget:   500,
		Context:  "some context",
		System:   "you are helpful",
	}
	if task.ID != "t1" {
		t.Errorf("ID: got %q", task.ID)
	}
	if task.Type != "code-gen" {
		t.Errorf("Type: got %q", task.Type)
	}
	if task.Repo != "chitinhq/octi-pulpo" {
		t.Errorf("Repo: got %q", task.Repo)
	}
	if len(task.Toolset) != 2 {
		t.Errorf("Toolset len: got %d", len(task.Toolset))
	}
	if task.Budget != 500 {
		t.Errorf("Budget: got %d", task.Budget)
	}
}

func TestAdapterResultFields(t *testing.T) {
	r := AdapterResult{
		TaskID:    "t1",
		Status:    "completed",
		Output:    "done",
		CostCents: 10,
		TokensIn:  100,
		TokensOut: 200,
		Adapter:   "anthropic",
		Error:     "",
	}
	if r.TaskID != "t1" {
		t.Errorf("TaskID: got %q", r.TaskID)
	}
	if r.Status != "completed" {
		t.Errorf("Status: got %q", r.Status)
	}
	if r.CostCents != 10 {
		t.Errorf("CostCents: got %d", r.CostCents)
	}
}

// --- Interface compliance test ---

type mockAdapter struct{ name string }

func (m *mockAdapter) Name() string { return m.name }
func (m *mockAdapter) CanAccept(_ *Task) bool { return true }
func (m *mockAdapter) Dispatch(_ context.Context, task *Task) (*AdapterResult, error) {
	return &AdapterResult{TaskID: task.ID, Status: "completed", Adapter: m.name}, nil
}

func TestAdapterInterface(t *testing.T) {
	var a Adapter = &mockAdapter{name: "mock"}
	task := &Task{ID: "x", Prompt: "hello"}
	res, err := a.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("Status: got %q", res.Status)
	}
	if !a.CanAccept(task) {
		t.Error("expected CanAccept true")
	}
}

// --- AnthropicAdapter tests ---

func TestAnthropicAdapterName(t *testing.T) {
	a := NewAnthropicAdapter("", "")
	if a.Name() != "anthropic" {
		t.Errorf("Name: got %q", a.Name())
	}
}

func TestAnthropicAdapterCanAccept_NoKey(t *testing.T) {
	os.Unsetenv("ANTHROPIC_API_KEY")
	a := NewAnthropicAdapter("", "")
	task := &Task{ID: "t1", Prompt: "hello"}
	if a.CanAccept(task) {
		t.Error("expected CanAccept false when ANTHROPIC_API_KEY is unset")
	}
}

func TestAnthropicAdapterDefaults(t *testing.T) {
	a := NewAnthropicAdapter("", "")
	if a.shellforge != defaultShellforge {
		t.Errorf("shellforge default: got %q", a.shellforge)
	}
	if a.model != defaultModel {
		t.Errorf("model default: got %q", a.model)
	}
}

// --- GHActionsAdapter tests ---

func TestGHActionsAdapterName(t *testing.T) {
	g := NewGHActionsAdapter("token")
	if g.Name() != "gh-actions" {
		t.Errorf("Name: got %q", g.Name())
	}
}

func TestGHActionsAdapterCanAccept(t *testing.T) {
	g := NewGHActionsAdapter("mytoken")
	task := &Task{ID: "t1", Repo: "chitinhq/octi-pulpo"}
	if !g.CanAccept(task) {
		t.Error("expected CanAccept true with token + repo")
	}
}

func TestGHActionsAdapterCanAccept_NoRepo(t *testing.T) {
	g := NewGHActionsAdapter("mytoken")
	task := &Task{ID: "t1", Repo: ""}
	if g.CanAccept(task) {
		t.Error("expected CanAccept false when repo is empty")
	}
}

func TestGHActionsAdapterCanAccept_NoToken(t *testing.T) {
	os.Unsetenv("GITHUB_TOKEN")
	g := NewGHActionsAdapter("")
	task := &Task{ID: "t1", Repo: "chitinhq/octi-pulpo"}
	if g.CanAccept(task) {
		t.Error("expected CanAccept false when token is empty")
	}
}

func TestGHActionsAdapterDispatch(t *testing.T) {
	var received ghDispatchPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %q", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer testtoken" {
			t.Errorf("auth header: got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("accept header: got %q", r.Header.Get("Accept"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("unmarshal body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	g := NewGHActionsAdapter("testtoken")
	g.baseURL = srv.URL

	task := &Task{
		ID:       "task-99",
		Type:     "pr-review",
		Repo:     "chitinhq/octi-pulpo",
		Prompt:   "review this PR",
		Toolset:  []string{"read_file"},
		Priority: "high",
	}

	res, err := g.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "queued" {
		t.Errorf("Status: got %q", res.Status)
	}
	if res.CostCents != 0 {
		t.Errorf("CostCents: got %d", res.CostCents)
	}
	if res.Adapter != "gh-actions" {
		t.Errorf("Adapter: got %q", res.Adapter)
	}

	// Validate payload sent to mock server
	if received.EventType != "octi-pulpo-dispatch" {
		t.Errorf("event_type: got %q", received.EventType)
	}
	if received.ClientPayload.TaskID != "task-99" {
		t.Errorf("task_id: got %q", received.ClientPayload.TaskID)
	}
	if received.ClientPayload.Type != "pr-review" {
		t.Errorf("type: got %q", received.ClientPayload.Type)
	}
	if received.ClientPayload.Priority != "high" {
		t.Errorf("priority: got %q", received.ClientPayload.Priority)
	}
	if len(received.ClientPayload.Toolset) != 1 || received.ClientPayload.Toolset[0] != "read_file" {
		t.Errorf("toolset: got %v", received.ClientPayload.Toolset)
	}
}
