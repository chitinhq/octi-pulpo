package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	claudeCodeTimeout       = 30 * time.Minute
	defaultClaudeCodeBinary = "claude"
)

// ClaudeCodeAdapter dispatches tasks to the Claude Code CLI agent (`claude -p`).
// Each task runs in an isolated git worktree. After dispatch the worktree is
// cleaned up regardless of outcome.
type ClaudeCodeAdapter struct {
	binary    string // path to claude binary
	workspace string // root workspace path
	router    *ModelRouter

	// shipBranch pushes the branch and opens a PR. Separated so tests can
	// inject failures (the silent-loss regression surface for #241).
	// Returns (output, err). A non-nil err means the work did NOT reach the
	// remote — callers MUST flip result.Status to "failed".
	shipBranch func(ctx context.Context, worktreePath, branchName, repo, prTitle, prBody string) (string, error)
}

// NewClaudeCodeAdapter creates a ClaudeCodeAdapter. Zero-value strings fall
// back to defaults: binary="claude", workspace="$HOME/workspace".
func NewClaudeCodeAdapter(binary, workspace string) *ClaudeCodeAdapter {
	if binary == "" {
		binary = defaultClaudeCodeBinary
	}
	if workspace == "" {
		workspace = filepath.Join(os.Getenv("HOME"), "workspace")
	}
	return &ClaudeCodeAdapter{
		binary:     binary,
		workspace:  workspace,
		router:     NewModelRouter(),
		shipBranch: defaultShipBranch,
	}
}

// defaultShipBranch is the production implementation of shipBranch: it runs
// `git push -u origin <branch>` and `gh pr create`. If either step fails, it
// returns a non-nil error so the caller can mark the dispatch as "failed"
// (honest-dispatch contract — see issue #241, PR #245).
func defaultShipBranch(ctx context.Context, worktreePath, branchName, repo, prTitle, prBody string) (string, error) {
	pushCmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", branchName)
	pushCmd.Dir = worktreePath
	if pushOut, pushErr := pushCmd.CombinedOutput(); pushErr != nil {
		return string(pushOut), fmt.Errorf("push failed: %s: %s", pushErr, string(pushOut))
	}
	prCmd := exec.CommandContext(ctx, "gh", "pr", "create",
		"--repo", repo,
		"--head", branchName,
		"--title", prTitle,
		"--body", prBody,
	)
	prCmd.Dir = worktreePath
	prOut, prErr := prCmd.CombinedOutput()
	if prErr != nil {
		return string(prOut), fmt.Errorf("pr create failed: %s: %s", prErr, string(prOut))
	}
	return string(prOut), nil
}

// Name returns the adapter identifier.
func (a *ClaudeCodeAdapter) Name() string { return "claude-code" }

// CanAccept returns true for task types that the Claude Code CLI handles well.
// Claude Code CLI authenticates via Max plan OAuth — no API key required.
func (a *ClaudeCodeAdapter) CanAccept(task *Task) bool {
	switch task.Type {
	case "code-gen", "bugfix", "qa", "plan", "groom", "validate", "pr-review", "triage":
		return true
	default:
		return false
	}
}


// buildArgs constructs the `claude` CLI argument list for the given task and
// worktree path.
func (a *ClaudeCodeAdapter) buildArgs(task *Task, worktreePath string) []string {
	model := a.router.ClaudeModel(task.Context)
	maxTurns := maxTurnsForType(task.Type)

	args := []string{
		"-p", task.Prompt,
		"--model", model,
		"--dangerously-skip-permissions",
		"--max-turns", fmt.Sprintf("%d", maxTurns),
		"--output-format", "json",
	}

	// Include MCP config if it exists in the worktree.
	mcpConfig := filepath.Join(worktreePath, "mcp-swarm.json")
	if _, err := os.Stat(mcpConfig); err == nil {
		args = append(args, "--mcp-config", mcpConfig)
	}

	return args
}

// Dispatch runs the Claude Code CLI in an isolated git worktree to implement
// the task. The worktree is removed on return regardless of outcome.
func (a *ClaudeCodeAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
	ctx, cancel := context.WithTimeout(ctx, claudeCodeTimeout)
	defer cancel()

	result := &AdapterResult{
		TaskID:  task.ID,
		Adapter: a.Name(),
	}

	// Resolve local repo path.
	repoPath := a.workspace
	if task.Repo != "" {
		repoName := filepath.Base(task.Repo)
		repoPath = filepath.Join(a.workspace, repoName)
	}

	// Create a git worktree for isolation so changes don't pollute the
	// main working tree.
	branchName := fmt.Sprintf("claude-code/%s", sanitizeBranch(task.ID))
	worktreePath := filepath.Join(a.workspace, ".worktrees", branchName)

	defaultBranch := detectDefaultBranch(repoPath)
	// Serialize `git worktree add` per-repo so parallel dispatches don't
	// race each other on .git/config.lock. Release *before* the long-running
	// CLI run so throughput isn't tanked per-repo. See repolock.go.
	releaseRepo, lockErr := repoLock(repoPath)
	if lockErr != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("%s: acquire repo lock: %v", ErrWorktreeRace, lockErr)
		return result, nil
	}
	wtCmd := exec.CommandContext(ctx, "git", "worktree", "add", worktreePath, "-b", branchName, "origin/"+defaultBranch)
	wtCmd.Dir = repoPath
	wtOut, wtErr := wtCmd.CombinedOutput()
	releaseRepo()
	if wtErr != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("%s: git worktree add %s on origin/%s: %s: %s",
			ErrWorktreeRace, worktreePath, defaultBranch, wtErr, string(wtOut))
		return result, nil
	}
	defer cleanupWorktree(repoPath, worktreePath, branchName)

	args := a.buildArgs(task, worktreePath)

	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = worktreePath
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	result.Output = string(out)

	if err != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("claude exited: %v", err)
		return result, nil
	}

	// CLI exited 0 — but dispatch is only "completed" once the work reaches
	// the remote. A locally-committed branch that never pushed is silent-loss:
	// downstream agents (QA, reviewer, merger) key off origin + PR, not the
	// worktree that cleanupWorktree is about to delete. See issue #241.
	if !hasNewCommits(worktreePath, "origin/"+defaultBranch) {
		// No commits produced — nothing to ship. Treat as completed; the
		// CLI ran to success and there was simply no diff. (Matches prior
		// behaviour; this branch is not the silent-loss surface.)
		result.Status = "completed"
		return result, nil
	}

	prTitle := truncate(task.Prompt, 60)
	prBody := fmt.Sprintf("Auto-generated by Claude Code via Octi Pulpo dispatch\n\nTask: %s\nAdapter: %s\nType: %s", task.ID, a.Name(), task.Type)
	ship := a.shipBranch
	if ship == nil {
		ship = defaultShipBranch
	}
	shipOut, shipErr := ship(ctx, worktreePath, branchName, task.Repo, prTitle, prBody)
	if shipErr != nil {
		// Honest-dispatch: push or PR-create failed, so the branch never
		// reached origin. Flip Status to "failed" so the outer dispatcher
		// reports Action="failed" rather than silent-loss "dispatched".
		result.Status = "failed"
		result.Error = shipErr.Error()
		if shipOut != "" {
			result.Output = shipOut
		}
		return result, nil
	}
	result.Status = "completed"
	result.Output = shipOut
	return result, nil
}
