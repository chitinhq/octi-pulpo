package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/learner"
)

const (
	defaultShellforge = "shellforge"
	defaultModel      = "claude-3-haiku-20241022"
	anthropicTimeout  = 5 * time.Minute
)

// AnthropicAdapter dispatches tasks to ShellForge using the Anthropic API.
type AnthropicAdapter struct {
	shellforge string
	model      string
	learner    *learner.Learner // nil = no auto-store
}

// SetLearner enables automatic episodic memory storage for task outcomes.
func (a *AnthropicAdapter) SetLearner(l *learner.Learner) {
	a.learner = l
}

// NewAnthropicAdapter creates an AnthropicAdapter. shellforge defaults to
// "shellforge" (resolved from PATH) and model defaults to
// "claude-3-haiku-20241022".
func NewAnthropicAdapter(shellforge, model string) *AnthropicAdapter {
	if shellforge == "" {
		shellforge = defaultShellforge
	}
	if model == "" {
		model = defaultModel
	}
	return &AnthropicAdapter{shellforge: shellforge, model: model}
}

// Name returns the adapter identifier.
func (a *AnthropicAdapter) Name() string {
	return "anthropic"
}

// CanAccept returns true when ANTHROPIC_API_KEY is set in the environment.
func (a *AnthropicAdapter) CanAccept(_ *Task) bool {
	return os.Getenv("ANTHROPIC_API_KEY") != ""
}

// Dispatch runs `shellforge agent --provider anthropic "<prompt>"` as a
// subprocess and returns the captured output. A 5-minute timeout is applied
// via the derived context.
func (a *AnthropicAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
	ctx, cancel := context.WithTimeout(ctx, anthropicTimeout)
	defer cancel()

	args := []string{
		"agent",
		"--provider", "anthropic",
		"--model", a.model,
		task.Prompt,
	}

	cmd := exec.CommandContext(ctx, a.shellforge, args...)
	cmd.Env = os.Environ()

	if task.Repo != "" {
		// Derive local repo path: $HOME/<basename(repo)>
		repoName := filepath.Base(task.Repo)
		repoPath := filepath.Join(os.Getenv("HOME"), repoName)
		cmd.Dir = repoPath
	}

	// Pre-dispatch: recall similar past tasks to enrich context.
	taskInfo := &learner.TaskInfo{
		Type: task.Type, Repo: task.Repo, Prompt: task.Prompt, Priority: task.Priority,
	}
	if a.learner != nil && task.System == "" {
		if prior := a.learner.RecallSimilar(ctx, taskInfo); prior != "" {
			task.System = prior
		}
	}

	out, err := cmd.CombinedOutput()

	result := &AdapterResult{
		TaskID:  task.ID,
		Adapter: a.Name(),
		Output:  string(out),
	}

	if err != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("shellforge exited: %v", err)
	} else {
		result.Status = "completed"
	}

	// Post-dispatch: auto-store outcome in episodic memory.
	if a.learner != nil {
		outcomeInfo := &learner.OutcomeInfo{
			Status: result.Status, Adapter: result.Adapter,
			TokensIn: result.TokensIn, TokensOut: result.TokensOut,
			CostCents: result.CostCents, Output: result.Output, Error: result.Error,
		}
		_ = a.learner.RecordOutcome(ctx, taskInfo, outcomeInfo)
	}

	return result, nil
}
