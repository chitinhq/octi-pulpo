package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
		Repo:   "AgentGuardHQ/octi-pulpo",
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

// ---- helpers ----

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
