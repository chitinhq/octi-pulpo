package dispatch

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/learner"
)

const (
	defaultClawtaModel    = "deepseek-chat"
	clawtaTimeout         = 10 * time.Minute
	defaultClawtaBinary   = "clawta"
	defaultClawtaProvider = "deepseek"

	// Ollama Cloud (OpenAI-compatible) — flat $20/mo, wired in openclaw.
	ollamaCloudBaseURL      = "https://ollama.com/v1"
	defaultOllamaCloudModel = "gpt-oss:20b"

	// Local Ollama — zero-cost fallback.
	localOllamaHost         = "127.0.0.1:11434"
	defaultLocalOllamaModel = "qwen2.5-coder:7b"
)

// ClawtaAdapter dispatches tasks to the Clawta governed CLI agent.
// Clawta runs in an isolated git worktree, implements the task,
// commits, pushes a branch, and opens a PR.
type ClawtaAdapter struct {
	binary    string // path to clawta binary
	model     string // model passed to Clawta (may be overridden by auto-selection)
	provider  string // provider passed to Clawta (may be overridden by auto-selection)
	workspace string // root workspace path
	learner   *learner.Learner
}

// NewClawtaAdapter creates a ClawtaAdapter. Zero-value strings fall back to
// defaults: binary="clawta", model="deepseek-chat", provider="deepseek",
// workspace="$HOME/workspace". At dispatch time the adapter auto-selects an
// inference provider (local Ollama → Ollama Cloud → DeepSeek) unless the
// caller constructed it with an explicit non-default provider override.
func NewClawtaAdapter(binary, model, provider, workspace string) *ClawtaAdapter {
	if binary == "" {
		binary = defaultClawtaBinary
	}
	if model == "" {
		model = defaultClawtaModel
	}
	if provider == "" {
		provider = defaultClawtaProvider
	}
	if workspace == "" {
		workspace = filepath.Join(os.Getenv("HOME"), "workspace")
	}
	return &ClawtaAdapter{
		binary:    binary,
		model:     model,
		provider:  provider,
		workspace: workspace,
	}
}

// Name returns the adapter identifier.
func (a *ClawtaAdapter) Name() string { return "clawta" }

// SetLearner enables automatic episodic memory storage for task outcomes.
func (a *ClawtaAdapter) SetLearner(l *learner.Learner) { a.learner = l }

// providerChoice describes the concrete provider/model/auth passed to clawta.
type providerChoice struct {
	name    string // telemetry label: "ollama-local", "ollama-cloud", "deepseek"
	flag    string // clawta --provider value
	model   string
	baseURL string // empty = provider default
	envKey  string // env var name carrying the API key on the child process
	envVal  string // API key value (may be empty for local ollama)
}

// selectProvider picks an inference provider in preference order:
//  1. local Ollama reachable at 127.0.0.1:11434 (free)
//  2. OLLAMA_CLOUD_API_KEY set (flat $20/mo)
//  3. DEEPSEEK_API_KEY set (legacy, kept for when wallet refills)
//
// Returns nil if none are available.
func selectProvider() *providerChoice {
	if localOllamaReachable() {
		return &providerChoice{
			name:    "ollama-local",
			flag:    "ollama",
			model:   envOr("CLAWTA_OLLAMA_LOCAL_MODEL", defaultLocalOllamaModel),
			baseURL: "http://" + localOllamaHost,
		}
	}
	if key := os.Getenv("OLLAMA_CLOUD_API_KEY"); key != "" {
		return &providerChoice{
			name:    "ollama-cloud",
			flag:    "openai",
			model:   envOr("CLAWTA_OLLAMA_CLOUD_MODEL", defaultOllamaCloudModel),
			baseURL: ollamaCloudBaseURL,
			envKey:  "OPENAI_API_KEY",
			envVal:  key,
		}
	}
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		return &providerChoice{
			name:   "deepseek",
			flag:   "deepseek",
			model:  defaultClawtaModel,
			envKey: "DEEPSEEK_API_KEY",
			envVal: key,
		}
	}
	return nil
}

// localOllamaReachable returns true if the local Ollama daemon answers on
// 127.0.0.1:11434 within a tight timeout. Set CLAWTA_SKIP_LOCAL_OLLAMA=1 to
// force the selector past the local probe (useful in tests and when the user
// wants to exercise a cloud provider on a laptop where Ollama is running).
func localOllamaReachable() bool {
	if os.Getenv("CLAWTA_SKIP_LOCAL_OLLAMA") == "1" {
		return false
	}
	conn, err := net.DialTimeout("tcp", localOllamaHost, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// CanAccept returns true for code-gen, bugfix, config, and evolve task types
// when at least one inference provider is available (local Ollama, Ollama
// Cloud, or DeepSeek).
func (a *ClawtaAdapter) CanAccept(task *Task) bool {
	if selectProvider() == nil {
		return false
	}
	switch task.Type {
	case "code-gen", "bugfix", "config", "evolve", "prompt_config", "tool_addition", "config_change":
		return true
	default:
		return false
	}
}

// Dispatch runs Clawta in an isolated git worktree to implement the task.
// On success the branch is pushed and a PR is opened. The worktree is removed
// on return regardless of outcome.
func (a *ClawtaAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
	ctx, cancel := context.WithTimeout(ctx, clawtaTimeout)
	defer cancel()

	result := &AdapterResult{
		TaskID:  task.ID,
		Adapter: a.Name(),
	}

	choice := selectProvider()
	if choice == nil {
		result.Status = "failed"
		result.Error = "no inference provider available (need OLLAMA_CLOUD_API_KEY, DEEPSEEK_API_KEY, or local Ollama at 127.0.0.1:11434)"
		return result, nil
	}

	// Resolve local repo path.
	repoPath := a.workspace
	if task.Repo != "" {
		repoName := filepath.Base(task.Repo)
		repoPath = filepath.Join(a.workspace, repoName)
	}

	// Create a git worktree for isolation so changes don't pollute the
	// main working tree.
	branchName := fmt.Sprintf("clawta/%s", sanitizeBranch(task.ID))
	worktreePath := filepath.Join(a.workspace, ".worktrees", branchName)

	defaultBranch := detectDefaultBranch(repoPath)
	wtCmd := exec.CommandContext(ctx, "git", "worktree", "add", worktreePath, "-b", branchName, "origin/"+defaultBranch)
	wtCmd.Dir = repoPath
	if wtOut, wtErr := wtCmd.CombinedOutput(); wtErr != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("worktree create failed: %s: %s", wtErr, string(wtOut))
		return result, nil
	}
	defer cleanupWorktree(repoPath, worktreePath, branchName)

	// Optionally prepend recalled prior experience to the prompt.
	prompt := task.Prompt
	if a.learner != nil {
		taskInfo := &learner.TaskInfo{
			Type: task.Type, Repo: task.Repo, Prompt: task.Prompt, Priority: task.Priority,
		}
		if prior := a.learner.RecallSimilar(ctx, taskInfo); prior != "" {
			prompt = prior + "\n\n" + prompt
		}
	}

	// Tell Clawta to commit but NOT to push or open PRs — the adapter
	// handles git plumbing after the agent finishes.
	prompt += "\n\nAfter implementing, stage and commit your changes with a descriptive message. Do NOT push or open PRs."

	args := []string{
		"run",
		"--provider", choice.flag,
		"--model", choice.model,
		"--max-turns", "100",
		"--timeout", fmt.Sprintf("%d", int(clawtaTimeout.Milliseconds())),
	}
	if choice.baseURL != "" {
		args = append(args, "--base-url", choice.baseURL)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = worktreePath

	env := os.Environ()
	if choice.envKey != "" && choice.envVal != "" {
		env = append(env, fmt.Sprintf("%s=%s", choice.envKey, choice.envVal))
	}
	// Propagate dispatch_id (octi#258) so downstream sentinel reconciler
	// can join Redis dispatch-log ↔ Neon execution_events.
	if task.DispatchID != "" {
		env = append(env, fmt.Sprintf("OCTI_DISPATCH_ID=%s", task.DispatchID))
	}
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	result.Output = string(out)

	if err != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("clawta exited (provider=%s): %v", choice.name, err)
	} else {
		result.Status = "completed"
	}

	// Adapter-side git plumbing: push branch and open PR if Clawta produced commits.
	if result.Status == "completed" {
		if hasNewCommits(worktreePath, "origin/"+defaultBranch) {
			pushCmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", branchName)
			pushCmd.Dir = worktreePath
			if pushOut, pushErr := pushCmd.CombinedOutput(); pushErr != nil {
				result.Error = fmt.Sprintf("push failed: %s: %s", pushErr, string(pushOut))
			} else {
				prTitle := truncate(task.Prompt, 60)
				prBody := fmt.Sprintf("Auto-generated by Clawta via Octi Pulpo dispatch\n\nTask: %s\nAdapter: %s\nType: %s\nProvider: %s\nDispatchID: %s", task.ID, a.Name(), task.Type, choice.name, task.DispatchID)
				prCmd := exec.CommandContext(ctx, "gh", "pr", "create",
					"--repo", task.Repo,
					"--head", branchName,
					"--title", prTitle,
					"--body", prBody,
				)
				prCmd.Dir = worktreePath
				if prOut, prErr := prCmd.CombinedOutput(); prErr != nil {
					result.Error = fmt.Sprintf("pr create failed: %s: %s", prErr, string(prOut))
				} else {
					result.Output = string(prOut)
				}
			}
		}
	}

	// Record outcome in episodic memory for future recall.
	if a.learner != nil {
		taskInfo := &learner.TaskInfo{
			Type: task.Type, Repo: task.Repo, Prompt: task.Prompt, Priority: task.Priority,
		}
		outcomeInfo := &learner.OutcomeInfo{
			Status:    result.Status,
			Adapter:   result.Adapter,
			TokensIn:  result.TokensIn,
			TokensOut: result.TokensOut,
			CostCents: result.CostCents,
			Output:    truncate(result.Output, 2000),
			Error:     result.Error,
		}
		_ = a.learner.RecordOutcome(ctx, taskInfo, outcomeInfo)
	}

	return result, nil
}

// sanitizeBranch converts a task ID to a valid git branch-name segment.
func sanitizeBranch(id string) string {
	r := strings.NewReplacer(" ", "-", "/", "-", ":", "-", ".", "-")
	return r.Replace(strings.ToLower(id))
}

// detectDefaultBranch checks whether the repo uses "main" or "master" as its
// default remote branch.
func detectDefaultBranch(repoPath string) string {
	cmd := exec.Command("git", "rev-parse", "--verify", "origin/main")
	cmd.Dir = repoPath
	if err := cmd.Run(); err == nil {
		return "main"
	}
	return "master"
}

// hasNewCommits returns true if the worktree has commits beyond the base ref.
func hasNewCommits(worktreePath, baseRef string) bool {
	cmd := exec.Command("git", "log", baseRef+"..HEAD", "--oneline")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// cleanupWorktree removes the worktree and local branch (best-effort).
func cleanupWorktree(repoPath, worktreePath, branchName string) {
	exec.Command("git", "worktree", "remove", "--force", worktreePath).Run() //nolint:errcheck
	exec.Command("git", "-C", repoPath, "branch", "-D", branchName).Run()    //nolint:errcheck
}

// truncate is defined in leaderboard.go (shared within the dispatch package).
