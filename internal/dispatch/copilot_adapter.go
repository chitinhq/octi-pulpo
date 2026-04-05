package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/learner"
)

const (
	defaultCopilotBaseURL = "https://api.githubcopilot.com"
	copilotTimeout        = 5 * time.Minute
)

// CopilotAdapter dispatches tasks to GitHub Copilot SDK with BYOK (Bring Your Own Key)
// support for enterprise customers.
type CopilotAdapter struct {
	apiKey  string
	baseURL string
	learner *learner.Learner // nil = no auto-store
}

// NewCopilotAdapter creates a CopilotAdapter. If apiKey is empty, the value
// of COPILOT_API_KEY environment variable is used.
func NewCopilotAdapter(apiKey string) *CopilotAdapter {
	if apiKey == "" {
		apiKey = os.Getenv("COPILOT_API_KEY")
	}
	return &CopilotAdapter{
		apiKey:  apiKey,
		baseURL: defaultCopilotBaseURL,
	}
}

// SetLearner enables automatic episodic memory storage for task outcomes.
func (c *CopilotAdapter) SetLearner(l *learner.Learner) {
	c.learner = l
}

// Name returns the adapter identifier.
func (c *CopilotAdapter) Name() string {
	return "copilot"
}

// CanAccept returns true when an API key is available.
// Copilot SDK is suitable for code generation and review tasks.
func (c *CopilotAdapter) CanAccept(task *Task) bool {
	return c.apiKey != "" && task != nil
}

// copilotCompletionRequest represents a completion request to Copilot SDK.
type copilotCompletionRequest struct {
	Model       string    `json:"model"`
	Prompt      string    `json:"prompt"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
}

// copilotCompletionResponse represents a completion response from Copilot SDK.
type copilotCompletionResponse struct {
	Choices []struct {
		Text string `json:"text"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Dispatch sends a completion request to Copilot SDK and returns the result.
func (c *CopilotAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
	ctx, cancel := context.WithTimeout(ctx, copilotTimeout)
	defer cancel()

	// Pre-dispatch: recall similar past tasks to enrich context.
	taskInfo := &learner.TaskInfo{
		Type: task.Type, Repo: task.Repo, Prompt: task.Prompt, Priority: task.Priority,
	}
	if c.learner != nil && task.System == "" {
		if prior := c.learner.RecallSimilar(ctx, taskInfo); prior != "" {
			task.System = prior
		}
	}

	// Build the prompt with system context if available
	fullPrompt := task.Prompt
	if task.System != "" {
		fullPrompt = task.System + "\n\n" + task.Prompt
	}

	reqBody := copilotCompletionRequest{
		Model:       "gpt-4", // Default model for Copilot SDK
		Prompt:      fullPrompt,
		MaxTokens:   4000,
		Temperature: 0.7,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("copilot: marshal request: %w", err)
	}

	url := c.baseURL + "/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("copilot: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: http request: %w", err)
	}
	defer resp.Body.Close()

	result := &AdapterResult{
		TaskID:  task.ID,
		Adapter: c.Name(),
	}

	if resp.StatusCode != http.StatusOK {
		result.Status = "failed"
		result.Error = fmt.Sprintf("API returned status %d", resp.StatusCode)
		return result, nil
	}

	var completionResp copilotCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completionResp); err != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("decode response: %v", err)
		return result, nil
	}

	if len(completionResp.Choices) == 0 {
		result.Status = "failed"
		result.Error = "no completion choices returned"
		return result, nil
	}

	result.Status = "completed"
	result.Output = completionResp.Choices[0].Text
	result.TokensIn = completionResp.Usage.PromptTokens
	result.TokensOut = completionResp.Usage.CompletionTokens
	// Estimate cost: $0.03 per 1K tokens for input, $0.06 per 1K tokens for output
	result.CostCents = int(float64(completionResp.Usage.PromptTokens)*0.03/1000*100) +
		int(float64(completionResp.Usage.CompletionTokens)*0.06/1000*100)

	// Post-dispatch: auto-store outcome in episodic memory.
	if c.learner != nil {
		outcomeInfo := &learner.OutcomeInfo{
			Status:    result.Status,
			Adapter:   result.Adapter,
			TokensIn:  result.TokensIn,
			TokensOut: result.TokensOut,
			CostCents: result.CostCents,
			Output:    result.Output,
			Error:     result.Error,
		}
		_ = c.learner.RecordOutcome(ctx, taskInfo, outcomeInfo)
	}

	return result, nil
}
