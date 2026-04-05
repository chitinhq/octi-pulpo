package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/learner"
)

const (
	defaultDeepSeekModel = "deepseek-coder"
	deepseekTimeout      = 5 * time.Minute
	deepseekBaseURL      = "https://api.deepseek.com/v1"
)

// DeepSeekAdapter dispatches tasks to ShellForge using the DeepSeek API via
// the OpenAI-compatible endpoint.
type DeepSeekAdapter struct {
	shellforge string
	model      string
	learner    *learner.Learner // nil = no auto-store
}

// NewDeepSeekAdapter creates a DeepSeekAdapter. shellforge defaults to
// "shellforge" (resolved from PATH) and model defaults to "deepseek-coder".
func NewDeepSeekAdapter(shellforge, model string) *DeepSeekAdapter {
	if shellforge == "" {
		shellforge = defaultShellforge
	}
	if model == "" {
		model = defaultDeepSeekModel
	}
	return &DeepSeekAdapter{shellforge: shellforge, model: model}
}

// SetLearner enables automatic episodic memory storage for task outcomes.
func (a *DeepSeekAdapter) SetLearner(l *learner.Learner) {
	a.learner = l
}

// Name returns the adapter identifier.
func (a *DeepSeekAdapter) Name() string {
	return "deepseek"
}

// CanAccept returns true for triage and pr-review tasks when DEEPSEEK_API_KEY
// is set in the environment. DeepSeek is cost-optimised for these lighter
// tasks; heavier tasks (code-gen, bugfix, qa) are left for Anthropic tiers.
func (a *DeepSeekAdapter) CanAccept(task *Task) bool {
	if os.Getenv("DEEPSEEK_API_KEY") == "" {
		return false
	}
	switch task.Type {
	case "triage", "pr-review":
		return true
	default:
		return false
	}
}

// Dispatch runs `shellforge agent --provider openai --model <model> "<prompt>"`
// as a subprocess with the DeepSeek base URL injected via OPENAI_BASE_URL.
// A 5-minute timeout is applied via the derived context.
func (a *DeepSeekAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
	ctx, cancel := context.WithTimeout(ctx, deepseekTimeout)
	defer cancel()

	args := []string{
		"agent",
		"--provider", "openai",
		"--model", a.model,
		task.Prompt,
	}

	cmd := exec.CommandContext(ctx, a.shellforge, args...)

	// Inject DeepSeek credentials and base URL into the subprocess environment.
	env := os.Environ()
	env = append(env, fmt.Sprintf("OPENAI_BASE_URL=%s", deepseekBaseURL))
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		env = append(env, fmt.Sprintf("OPENAI_API_KEY=%s", key))
	}
	cmd.Env = env

	if task.Repo != "" {
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
