package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestClaudeCodeAdapter_ShipFailure_SilentLossRegression is the permanent
// regression guard for chitinhq/octi#241 (companion to PR #245's
// TestDispatchBudget_CallsAdapter_SilentLossRegression).
//
// The lie: Dispatch() set result.Status = "completed" the moment the claude
// CLI exited 0, then attempted git push + gh pr create. If either of those
// shipping steps failed, result.Error was populated but Status stayed
// "completed" — so the outer dispatcher happily reported action="dispatched"
// for a run whose commits never reached origin. Downstream agents key off a
// PR existing, not a worktree (which cleanupWorktree then deletes).
//
// The fix: shipBranch failures flip Status → "failed". This test injects a
// failing shipBranch via the adapter's seam and asserts Status=="failed".
// It builds a real local repo + fake `claude` binary so the Dispatch codepath
// runs end-to-end (worktree create, CLI exec with a commit, ship, cleanup).
//
// Build-fails against HEAD~1 (where Status stayed "completed" on ship error);
// passes against this fix.
func TestClaudeCodeAdapter_ShipFailure_SilentLossRegression(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based fake claude binary; unix only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	workspace := t.TempDir()
	repoName := "octi"
	repoPath := filepath.Join(workspace, repoName)

	// Build a bare "origin" and a local clone so `origin/<branch>` resolves.
	originPath := filepath.Join(workspace, "origin.git")
	mustRun(t, "", "git", "init", "--bare", "-b", "main", originPath)

	mustRun(t, "", "git", "init", "-b", "main", repoPath)
	mustRun(t, repoPath, "git", "config", "user.email", "test@example.com")
	mustRun(t, repoPath, "git", "config", "user.name", "test")
	mustRun(t, repoPath, "git", "remote", "add", "origin", originPath)
	// Seed commit so origin/main exists.
	seed := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(seed, []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repoPath, "git", "add", ".")
	mustRun(t, repoPath, "git", "commit", "-m", "seed")
	mustRun(t, repoPath, "git", "push", "-u", "origin", "main")

	// Fake `claude` binary: makes a commit in its CWD (the worktree) and
	// exits 0. This produces the "hasNewCommits == true" condition so
	// Dispatch reaches the shipBranch call (the silent-loss surface).
	fakeBin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
set -e
echo "hello" > claude-output.txt
git config user.email "fake@claude"
git config user.name "fake-claude"
git add claude-output.txt
git commit -m "fake claude change" >/dev/null
echo '{"ok":true}'
`
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	a := NewClaudeCodeAdapter(fakeBin, workspace)

	shipCalls := 0
	a.shipBranch = func(ctx context.Context, worktreePath, branchName, repo, prTitle, prBody string) (string, error) {
		shipCalls++
		// Simulate `git push` being rejected by origin or `gh pr create`
		// failing — either way, the branch never reached the remote.
		return "remote rejected (pre-receive hook declined)", errors.New("push failed: exit status 1: remote rejected")
	}

	task := &Task{
		ID:      "silentloss-241-regression",
		Type:    "code-gen",
		Prompt:  "dummy prompt",
		Context: "low",
		Repo:    "chitinhq/" + repoName,
	}

	res, err := a.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("Dispatch returned unexpected err: %v", err)
	}
	if res == nil {
		t.Fatal("Dispatch returned nil result")
	}

	if shipCalls != 1 {
		t.Fatalf("shipBranch should be called exactly once, got %d (Dispatch never reached the ship step — fake claude commit may have failed)", shipCalls)
	}

	// THE assertion. Before the fix, Status was "completed" here.
	if res.Status != "failed" {
		t.Fatalf("SILENT-LOSS REGRESSION (#241): ship failed but Status=%q, want %q. result.Error=%q",
			res.Status, "failed", res.Error)
	}
	if res.Error == "" {
		t.Errorf("expected result.Error to carry ship failure reason, got empty string")
	}
}

// TestClaudeCodeAdapter_ShipSuccess_Completes is the happy-path companion:
// when shipBranch succeeds, Status must be "completed" with no Error.
func TestClaudeCodeAdapter_ShipSuccess_Completes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	workspace := t.TempDir()
	repoName := "octi"
	repoPath := filepath.Join(workspace, repoName)
	originPath := filepath.Join(workspace, "origin.git")
	mustRun(t, "", "git", "init", "--bare", "-b", "main", originPath)
	mustRun(t, "", "git", "init", "-b", "main", repoPath)
	mustRun(t, repoPath, "git", "config", "user.email", "test@example.com")
	mustRun(t, repoPath, "git", "config", "user.name", "test")
	mustRun(t, repoPath, "git", "remote", "add", "origin", originPath)
	seed := filepath.Join(repoPath, "README.md")
	_ = os.WriteFile(seed, []byte("seed\n"), 0o644)
	mustRun(t, repoPath, "git", "add", ".")
	mustRun(t, repoPath, "git", "commit", "-m", "seed")
	mustRun(t, repoPath, "git", "push", "-u", "origin", "main")

	fakeBin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
set -e
echo "hello" > claude-output.txt
git config user.email "fake@claude"
git config user.name "fake-claude"
git add claude-output.txt
git commit -m "fake claude change" >/dev/null
echo '{"ok":true}'
`
	_ = os.WriteFile(fakeBin, []byte(script), 0o755)

	a := NewClaudeCodeAdapter(fakeBin, workspace)
	a.shipBranch = func(ctx context.Context, worktreePath, branchName, repo, prTitle, prBody string) (string, error) {
		return "https://github.com/chitinhq/octi/pull/999", nil
	}

	task := &Task{
		ID:      "silentloss-241-happy",
		Type:    "code-gen",
		Prompt:  "dummy prompt",
		Context: "low",
		Repo:    "chitinhq/" + repoName,
	}

	res, err := a.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("Dispatch err: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("Status = %q, want %q (err=%q)", res.Status, "completed", res.Error)
	}
	if res.Error != "" {
		t.Errorf("Error should be empty on success, got %q", res.Error)
	}
}

func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, string(out))
	}
	_ = fmt.Sprintf // keep import used if future logging added
}
