package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCopilotCLIAdapter_Dispatch_PushFailure_SilentLossRegression pins the
// honest-dispatch contract for the copilot_cli adapter: when the agent CLI
// exits 0 AND produces a commit, but `git push` to origin fails, the adapter
// MUST return Status="failed" (NOT "completed"). Before chitinhq/octi#242's
// fix, Status was set to "completed" on CLI exit 0 and never reverted on push
// failure — the outer dispatcher (dispatcher.go:264) then mapped that to
// Action="dispatched" while the worktree was being torn down by
// cleanupWorktree, silently dropping the work. Mirrors the shape of
// TestDispatchBudget_CallsAdapter_SilentLossRegression from chitinhq/octi#245.
//
// refs: chitinhq/octi#242, chitinhq/octi#245, workspace#408
func TestCopilotCLIAdapter_Dispatch_PushFailure_SilentLossRegression(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binary not portable to windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// 1. Build a fake workspace with a repo whose `origin` is a bare remote
	//    configured to REJECT pushes (non-existent path). The `git push` in
	//    the adapter will fail deterministically.
	workspace := t.TempDir()
	repoName := "silentloss-242"
	repoPath := filepath.Join(workspace, repoName)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Initialize a local origin bare repo that we can push to (main branch).
	originPath := filepath.Join(workspace, "origin.git")
	runGit(t, workspace, "init", "--bare", "-b", "main", originPath)

	// Working repo with `main` branch + initial commit + origin pointing at
	// the bare repo. After the initial push, we DELETE origin.git so that
	// subsequent pushes fail — simulating a remote-reject / network-down path.
	runGit(t, repoPath, "init", "-b", "main")
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "init")
	runGit(t, repoPath, "remote", "add", "origin", originPath)
	runGit(t, repoPath, "push", "-u", "origin", "main")

	// Now nuke the origin so the adapter's `git push` fails.
	if err := os.RemoveAll(originPath); err != nil {
		t.Fatal(err)
	}

	// 2. Build a stub `copilot` binary that exits 0 AND produces a commit
	//    in cwd (which will be the adapter's worktree). This simulates the
	//    agent succeeding — the silent-loss gap is between CLI-exit-0 and
	//    origin actually receiving the branch.
	stubDir := t.TempDir()
	stubPath := filepath.Join(stubDir, "copilot")
	stub := `#!/bin/sh
# Write a file and commit it — so hasNewCommits() returns true and the
# adapter proceeds to push.
echo "work" > silentloss-work.txt
git add silentloss-work.txt >/dev/null 2>&1
git -c user.email=stub@example.com -c user.name=stub commit -m "stub work" >/dev/null 2>&1
echo '{"ok":true}'
exit 0
`
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	// 3. Dispatch. The adapter will:
	//    a) create a worktree off origin/main
	//    b) run the stub → exit 0 → Status="completed"
	//    c) hasNewCommits → true (stub committed)
	//    d) `git push` → FAIL (origin.git was deleted)
	//    e) MUST flip Status to "failed" (the regression contract)
	a := NewCopilotCLIAdapter(stubPath, workspace)
	task := &Task{
		ID:      fmt.Sprintf("silentloss-242-%d", time.Now().UnixNano()),
		Type:    "bugfix",
		Repo:    "chitinhq/" + repoName,
		Prompt:  "regression test — push will fail",
		Context: "low",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := a.Dispatch(ctx, task)
	if err != nil {
		t.Fatalf("Dispatch returned transport error: %v", err)
	}
	if result == nil {
		t.Fatal("Dispatch returned nil result")
	}

	// THE assertion: on push failure, Status must be "failed", not "completed".
	// This is the honest-dispatch contract the fix in copilot_cli_adapter.go
	// establishes. If this test fails with Status="completed", silent loss
	// has regressed.
	if result.Status != "failed" {
		t.Fatalf("silent-loss regression: push failed but Status=%q, want %q; Error=%q",
			result.Status, "failed", result.Error)
	}
	if !strings.Contains(result.Error, "push failed") {
		t.Errorf("expected Error to mention 'push failed', got %q", result.Error)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed in %s: %v: %s", args, dir, err, string(out))
	}
}
