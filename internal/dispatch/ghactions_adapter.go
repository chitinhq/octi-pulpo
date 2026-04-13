package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/chitinhq/octi-pulpo/internal/flow"
)

const defaultGHBaseURL = "https://api.github.com"

// GHActionsAdapter dispatches tasks by firing repository_dispatch events
// on the GitHub API. The workflow runs asynchronously — results are "queued".
type GHActionsAdapter struct {
	token   string
	baseURL string
}

// NewGHActionsAdapter creates a GHActionsAdapter. If token is empty the value
// of GITHUB_TOKEN environment variable is used.
func NewGHActionsAdapter(token string) *GHActionsAdapter {
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	return &GHActionsAdapter{
		token:   token,
		baseURL: defaultGHBaseURL,
	}
}

// Name returns the adapter identifier.
func (g *GHActionsAdapter) Name() string {
	return "gh-actions"
}

// CanAccept returns true when a token is available and task.Repo is non-empty.
func (g *GHActionsAdapter) CanAccept(task *Task) bool {
	return g.token != "" && task != nil && task.Repo != ""
}

type ghDispatchPayload struct {
	EventType     string          `json:"event_type"`
	ClientPayload ghClientPayload `json:"client_payload"`
}

type ghClientPayload struct {
	TaskID   string   `json:"task_id"`
	Type     string   `json:"type"`
	Prompt   string   `json:"prompt"`
	Toolset  []string `json:"toolset"`
	Priority string   `json:"priority"`
}

// Dispatch POSTs a repository_dispatch event to GitHub. On 204 the task is
// considered queued — the actual workflow result is asynchronous.
func (g *GHActionsAdapter) Dispatch(ctx context.Context, task *Task) (retResult *AdapterResult, retErr error) {
	defer flow.Span("swarm.dispatch.ghactions", map[string]interface{}{
		"task_id": task.ID, "type": task.Type, "repo": task.Repo, "priority": task.Priority,
	})(&retErr)

	payload := ghDispatchPayload{
		EventType: "octi-pulpo-dispatch",
		ClientPayload: ghClientPayload{
			TaskID:   task.ID,
			Type:     task.Type,
			Prompt:   task.Prompt,
			Toolset:  task.Toolset,
			Priority: task.Priority,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ghactions: marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/dispatches", g.baseURL, task.Repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ghactions: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ghactions: http: %w", err)
	}
	defer resp.Body.Close()

	result := &AdapterResult{
		TaskID:    task.ID,
		Adapter:   g.Name(),
		CostCents: 0,
	}

	if resp.StatusCode == http.StatusNoContent {
		result.Status = "queued"
		return result, nil
	}

	result.Status = "failed"
	result.Error = fmt.Sprintf("unexpected status %d", resp.StatusCode)
	return result, nil
}
