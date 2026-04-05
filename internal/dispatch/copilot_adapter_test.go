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

func TestCopilotAdapterName(t *testing.T) {
	c := NewCopilotAdapter("test-key")
	if c.Name() != "copilot" {
		t.Errorf("Name: got %q, want 'copilot'", c.Name())
	}
}

func TestCopilotAdapterCanAccept(t *testing.T) {
	// Test with API key
	c := NewCopilotAdapter("test-key")
	task := &Task{ID: "t1", Prompt: "hello"}
	if !c.CanAccept(task) {
		t.Error("expected CanAccept true with API key")
	}

	// Test without API key
	c2 := NewCopilotAdapter("")
	os.Unsetenv("COPILOT_API_KEY")
	if c2.CanAccept(task) {
		t.Error("expected CanAccept false without API key")
	}
}

func TestCopilotAdapterCanAccept_WithEnvVar(t *testing.T) {
	os.Setenv("COPILOT_API_KEY", "env-key")
	c := NewCopilotAdapter("") // Empty constructor should use env var
	task := &Task{ID: "t1", Prompt: "hello"}
	if !c.CanAccept(task) {
		t.Error("expected CanAccept true when API key from env var")
	}
	os.Unsetenv("COPILOT_API_KEY")
}

func TestCopilotAdapterDispatch(t *testing.T) {
	var receivedRequest struct {
		Model       string  `json:"model"`
		Prompt      string  `json:"prompt"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %q", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header: got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type header: got %q", r.Header.Get("Content-Type"))
		}

		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &receivedRequest); err != nil {
			t.Errorf("unmarshal request: %v", err)
		}

		// Return a mock response
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"text": "Here's the implementation you requested.",
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     100,
				"completion_tokens": 200,
				"total_tokens":      300,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewCopilotAdapter("test-key")
	c.baseURL = srv.URL

	task := &Task{
		ID:       "task-123",
		Type:     "code-gen",
		Repo:     "chitinhq/octi-pulpo",
		Prompt:   "Write a function to calculate factorial",
		Toolset:  []string{"read_file", "write_file"},
		Priority: "normal",
	}

	res, err := c.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != "completed" {
		t.Errorf("Status: got %q, want 'completed'", res.Status)
	}
	if res.Adapter != "copilot" {
		t.Errorf("Adapter: got %q, want 'copilot'", res.Adapter)
	}
	if res.Output != "Here's the implementation you requested." {
		t.Errorf("Output: got %q", res.Output)
	}
	if res.TokensIn != 100 {
		t.Errorf("TokensIn: got %d, want 100", res.TokensIn)
	}
	if res.TokensOut != 200 {
		t.Errorf("TokensOut: got %d, want 200", res.TokensOut)
	}
	if res.CostCents == 0 {
		t.Error("CostCents should be non-zero")
	}

	// Validate request sent to mock server
	if receivedRequest.Model != "gpt-4" {
		t.Errorf("model: got %q, want 'gpt-4'", receivedRequest.Model)
	}
	if receivedRequest.Prompt != task.Prompt {
		t.Errorf("prompt: got %q", receivedRequest.Prompt)
	}
	if receivedRequest.MaxTokens != 4000 {
		t.Errorf("max_tokens: got %d, want 4000", receivedRequest.MaxTokens)
	}
	if receivedRequest.Temperature != 0.7 {
		t.Errorf("temperature: got %f, want 0.7", receivedRequest.Temperature)
	}
}

func TestCopilotAdapterDispatch_WithSystemPrompt(t *testing.T) {
	var receivedRequest struct {
		Prompt string `json:"prompt"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedRequest)

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"text": "response"},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     50,
				"completion_tokens": 50,
				"total_tokens":      100,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewCopilotAdapter("test-key")
	c.baseURL = srv.URL

	task := &Task{
		ID:      "task-1",
		Prompt:  "Write code",
		System:  "You are a helpful assistant",
	}

	_, err := c.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that system prompt was prepended
	expectedPrompt := "You are a helpful assistant\n\nWrite code"
	if receivedRequest.Prompt != expectedPrompt {
		t.Errorf("prompt: got %q, want %q", receivedRequest.Prompt, expectedPrompt)
	}
}

func TestCopilotAdapterDispatch_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal server error"))
	}))
	defer srv.Close()

	c := NewCopilotAdapter("test-key")
	c.baseURL = srv.URL

	task := &Task{
		ID:     "task-err",
		Prompt: "test",
	}

	res, err := c.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != "failed" {
		t.Errorf("Status: got %q, want 'failed'", res.Status)
	}
	if res.Error == "" {
		t.Error("Error should be set for API error")
	}
}

func TestCopilotAdapterDispatch_NoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{},
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 0,
				"total_tokens":      10,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewCopilotAdapter("test-key")
	c.baseURL = srv.URL

	task := &Task{
		ID:     "task-no-choices",
		Prompt: "test",
	}

	res, err := c.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != "failed" {
		t.Errorf("Status: got %q, want 'failed'", res.Status)
	}
	if res.Error != "no completion choices returned" {
		t.Errorf("Error: got %q", res.Error)
	}
}
