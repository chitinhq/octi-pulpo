package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- Name ----

func TestClawtaAdapterName(t *testing.T) {
	a := NewClawtaAdapter("", "", "", "")
	if got := a.Name(); got != "clawta" {
		t.Errorf("Name(): want clawta, got %s", got)
	}
}

// ---- CanAccept ----

func TestClawtaAdapterCanAcceptCodeGen(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "code-gen"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(code-gen) with key: want true, got false")
	}
}

func TestClawtaAdapterCanAcceptBugfix(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "bugfix"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(bugfix) with key: want true, got false")
	}
}

func TestClawtaAdapterCanAcceptConfig(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "config"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(config) with key: want true, got false")
	}
}

func TestClawtaAdapterCanAcceptEvolve(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "evolve"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(evolve) with key: want true, got false")
	}
}

func TestClawtaAdapterCanAcceptEvolveSubTypes(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	for _, typ := range []string{"prompt_config", "tool_addition", "config_change"} {
		task := &Task{Type: typ}
		if !a.CanAccept(task) {
			t.Errorf("CanAccept(%s) with key: want true, got false", typ)
		}
	}
}

func TestClawtaAdapterRejectsPRReview(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "pr-review"}
	if a.CanAccept(task) {
		t.Error("CanAccept(pr-review): want false, got true")
	}
}

func TestClawtaAdapterRejectsTriage(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "triage"}
	if a.CanAccept(task) {
		t.Error("CanAccept(triage): want false, got true")
	}
}

func TestClawtaAdapterRejectsWithoutKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "code-gen"}
	if a.CanAccept(task) {
		t.Error("CanAccept(code-gen) without key: want false, got true")
	}
}

// ---- Defaults ----

func TestClawtaAdapterDefaults(t *testing.T) {
	a := NewClawtaAdapter("", "", "", "")
	if a.binary != defaultClawtaBinary {
		t.Errorf("default binary: want %s, got %s", defaultClawtaBinary, a.binary)
	}
	if a.model != defaultClawtaModel {
		t.Errorf("default model: want %s, got %s", defaultClawtaModel, a.model)
	}
	if a.provider != defaultClawtaProvider {
		t.Errorf("default provider: want %s, got %s", defaultClawtaProvider, a.provider)
	}
}

func TestClawtaAdapterCustomValues(t *testing.T) {
	a := NewClawtaAdapter("/usr/local/bin/clawta", "deepseek-v3", "anthropic", "/tmp/ws")
	if a.binary != "/usr/local/bin/clawta" {
		t.Errorf("binary: want /usr/local/bin/clawta, got %s", a.binary)
	}
	if a.model != "deepseek-v3" {
		t.Errorf("model: want deepseek-v3, got %s", a.model)
	}
	if a.provider != "anthropic" {
		t.Errorf("provider: want anthropic, got %s", a.provider)
	}
	if a.workspace != "/tmp/ws" {
		t.Errorf("workspace: want /tmp/ws, got %s", a.workspace)
	}
}

// ---- sanitizeBranch ----

func TestSanitizeBranch(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"task-001", "task-001"},
		{"Task 001", "task-001"},
		{"feat/add-clawta", "feat-add-clawta"},
		{"v1.2.3", "v1-2-3"},
		{"task:fix:bug", "task-fix-bug"},
		{"UPPER CASE", "upper-case"},
	}
	for _, tc := range cases {
		got := sanitizeBranch(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeBranch(%q): want %q, got %q", tc.in, tc.want, got)
		}
	}
}

// ---- detectDefaultBranch ----

// TestDetectDefaultBranchMaster verifies that detectDefaultBranch returns
// "master" when origin/main does not exist. Uses a real temp git repo with
// a "master" remote branch to avoid actually calling the clawta binary.
func TestDetectDefaultBranchMaster(t *testing.T) {
	// Build a bare "remote" repo with only a master branch.
	remoteDir := t.TempDir()
	mustGit(t, remoteDir, "init", "--bare")

	// Build a local clone that points at the bare remote.
	localDir := t.TempDir()
	mustGit(t, localDir, "init")
	mustGit(t, localDir, "remote", "add", "origin", remoteDir)

	// Create an initial commit so origin/master can be pushed.
	if err := os.WriteFile(filepath.Join(localDir, "README"), []byte("init"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, localDir, "-c", "user.email=test@test.com", "-c", "user.name=Test",
		"commit", "--allow-empty", "-m", "init")

	// Rename the default branch to master (git may default to main).
	mustGit(t, localDir, "branch", "-M", "master")
	mustGit(t, localDir, "push", "origin", "master")

	// origin/main does not exist, so detectDefaultBranch should return master.
	got := detectDefaultBranch(localDir)
	if got != "master" {
		t.Errorf("detectDefaultBranch: want master, got %s", got)
	}
}

// TestDetectDefaultBranchMain verifies that detectDefaultBranch returns "main"
// when origin/main exists.
func TestDetectDefaultBranchMain(t *testing.T) {
	remoteDir := t.TempDir()
	mustGit(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	mustGit(t, localDir, "init")
	mustGit(t, localDir, "remote", "add", "origin", remoteDir)

	if err := os.WriteFile(filepath.Join(localDir, "README"), []byte("init"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, localDir, "-c", "user.email=test@test.com", "-c", "user.name=Test",
		"commit", "--allow-empty", "-m", "init")

	// Ensure the branch is named "main".
	mustGit(t, localDir, "branch", "-M", "main")
	mustGit(t, localDir, "push", "origin", "main")

	got := detectDefaultBranch(localDir)
	if got != "main" {
		t.Errorf("detectDefaultBranch: want main, got %s", got)
	}
}

// ---- Dispatch (mocked subprocess) ----

// TestClawtaAdapterDispatchMissingBinary verifies that when the binary does not
// exist the result status is "failed" and no panic occurs.
// The worktree creation will also fail (no real git repo), which is the first
// failure surface — the adapter must handle it gracefully.
func TestClawtaAdapterDispatchFailsGracefully(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")

	// Use a tmp dir that is NOT a git repo so worktree add fails immediately.
	ws := t.TempDir()
	a := NewClawtaAdapter("clawta-does-not-exist", "", "", ws)

	task := &Task{
		ID:     "test-task-001",
		Type:   "code-gen",
		Repo:   "chitinhq/octi",
		Prompt: "Add a hello world function",
	}

	result, err := a.Dispatch(context.Background(), task)
	// err from Dispatch itself should be nil — failures are encoded in result.
	if err != nil {
		t.Fatalf("Dispatch returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Dispatch returned nil result")
	}
	if result.Status != "failed" {
		t.Errorf("Status: want failed, got %s", result.Status)
	}
	if result.Adapter != "clawta" {
		t.Errorf("Adapter: want clawta, got %s", result.Adapter)
	}
	if result.TaskID != "test-task-001" {
		t.Errorf("TaskID: want test-task-001, got %s", result.TaskID)
	}
}

// TestClawtaAdapterDispatch_HonestDispatch_SilentLossRegression asserts that
// when the clawta subprocess succeeds (Status transitions to "completed") but
// the adapter-side git push fails, result.Status is downgraded to "failed".
// Mirrors PR #245 style (chitinhq/octi#243) — gates success claim on the real
// side-effect actually landing. Build-fails against the pre-fix shape where
// push failure only populated result.Error and Status stayed "completed".
func TestClawtaAdapterDispatch_HonestDispatch_SilentLossRegression(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")

	// Build a fake `clawta` binary that always exits 0 after creating a commit
	// in cwd. The adapter is constructed with the full path to this shim, so
	// no PATH manipulation is needed. The real `git` is inherited from the
	// system PATH so push actually runs — it will fail because the remote is
	// a dangling path (deleted after the initial fetch).
	shimDir := t.TempDir()
	clawtaShim := filepath.Join(shimDir, "clawta")
	shim := "#!/bin/sh\n" +
		"echo 'fake clawta run' > file.txt\n" +
		"git -c user.email=fake@test -c user.name=Fake add file.txt\n" +
		"git -c user.email=fake@test -c user.name=Fake commit -m 'fake clawta commit'\n" +
		"exit 0\n"
	if err := os.WriteFile(clawtaShim, []byte(shim), 0755); err != nil {
		t.Fatal(err)
	}

	// Workspace layout: workspace/<repo-name> is a real git repo with a
	// remote pointing at a bare repo we will delete, forcing push to fail.
	ws := t.TempDir()
	repoName := "silentloss-target"
	repoPath := filepath.Join(ws, repoName)
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}
	remoteDir := filepath.Join(ws, "remote.git")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, remoteDir, "init", "--bare")

	// Init local repo with one commit on main and a remote tracking main.
	mustGit(t, repoPath, "init")
	mustGit(t, repoPath, "remote", "add", "origin", remoteDir)
	if err := os.WriteFile(filepath.Join(repoPath, "README"), []byte("init"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoPath, "-c", "user.email=t@t.com", "-c", "user.name=T", "add", "README")
	mustGit(t, repoPath, "-c", "user.email=t@t.com", "-c", "user.name=T", "commit", "-m", "init")
	mustGit(t, repoPath, "branch", "-M", "main")
	mustGit(t, repoPath, "push", "origin", "main")
	// Fetch to materialize refs/remotes/origin/main — the adapter worktrees
	// from origin/<defaultBranch>, so without this the test would fail at
	// worktree creation rather than at the push step it claims to exercise.
	mustGit(t, repoPath, "fetch", "origin", "main")

	// Nuke the remote so the adapter-side `git push` fails with a real error.
	if err := os.RemoveAll(remoteDir); err != nil {
		t.Fatal(err)
	}

	a := NewClawtaAdapter(clawtaShim, "", "", ws)
	task := &Task{
		ID:     "silent-loss-regression-243",
		Type:   "code-gen",
		Repo:   "chitinhq/" + repoName,
		Prompt: "Write hello world",
	}

	result, err := a.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("Dispatch returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Dispatch returned nil result")
	}

	// The lie: pre-fix, push failure only set result.Error and Status stayed
	// "completed". The honest-dispatch fix must downgrade to "failed".
	if result.Status != "failed" {
		t.Errorf("silent-loss regression: push failed but Status=%q (want \"failed\"). "+
			"Adapter claimed success for work that never reached origin. "+
			"result.Error=%q", result.Status, result.Error)
	}
	if result.Error == "" {
		t.Error("result.Error: want non-empty push-failure reason, got empty")
	}
	if !strings.Contains(result.Error, "push failed") {
		t.Errorf("result.Error: want contains \"push failed\", got %q", result.Error)
	}
}

// ---- helpers ----

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
