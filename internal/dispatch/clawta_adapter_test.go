package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// isolateProviderEnv clears all provider-selection inputs so a test case can
// re-assert exactly one. Local-Ollama is skipped by default via
// CLAWTA_SKIP_LOCAL_OLLAMA=1 — tests that want to exercise the local-Ollama
// branch must override that explicitly. DEEPSEEK_API_KEY is also cleared
// defensively so stray env state on the test host cannot smuggle the
// retired DeepSeek provider into any assertion.
func isolateProviderEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OLLAMA_CLOUD_API_KEY", "")
	t.Setenv("CLAWTA_OLLAMA_CLOUD_MODEL", "")
	t.Setenv("CLAWTA_SKIP_LOCAL_OLLAMA", "1")
}

// ---- Name ----

func TestClawtaAdapterName(t *testing.T) {
	a := NewClawtaAdapter("", "", "", "")
	if got := a.Name(); got != "clawta" {
		t.Errorf("Name(): want clawta, got %s", got)
	}
}

// ---- CanAccept ----

func TestClawtaAdapterCanAcceptCodeGen(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "code-gen"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(code-gen) with ollama-cloud key: want true, got false")
	}
}

func TestClawtaAdapterCanAcceptBugfix(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "bugfix"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(bugfix) with ollama-cloud key: want true, got false")
	}
}

func TestClawtaAdapterCanAcceptConfig(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "config"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(config) with ollama-cloud key: want true, got false")
	}
}

func TestClawtaAdapterCanAcceptEvolve(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "evolve"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(evolve) with ollama-cloud key: want true, got false")
	}
}

func TestClawtaAdapterCanAcceptEvolveSubTypes(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	for _, typ := range []string{"prompt_config", "tool_addition", "config_change"} {
		task := &Task{Type: typ}
		if !a.CanAccept(task) {
			t.Errorf("CanAccept(%s) with ollama-cloud key: want true, got false", typ)
		}
	}
}

func TestClawtaAdapterRejectsPRReview(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "pr-review"}
	if a.CanAccept(task) {
		t.Error("CanAccept(pr-review): want false, got true")
	}
}

func TestClawtaAdapterRejectsTriage(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "test-key")
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "triage"}
	if a.CanAccept(task) {
		t.Error("CanAccept(triage): want false, got true")
	}
}

// TestClawtaAdapterRejectsWithoutAnyProvider verifies that when all three
// providers are unavailable (no keys, local ollama probe skipped), CanAccept
// returns false even for supported task types.
func TestClawtaAdapterRejectsWithoutAnyProvider(t *testing.T) {
	isolateProviderEnv(t)
	a := NewClawtaAdapter("", "", "", "")
	task := &Task{Type: "code-gen"}
	if a.CanAccept(task) {
		t.Error("CanAccept(code-gen) with no provider: want false, got true")
	}
}

// ---- Provider selection (2-way: local Ollama → Ollama Cloud) ----

// TestSelectProviderDeepSeekRetired verifies that DEEPSEEK_API_KEY alone is
// NOT sufficient to select a provider — DeepSeek was removed from the T1
// cascade on 2026-04-15 (provider policy: Ollama Cloud + local GPU only).
func TestSelectProviderDeepSeekRetired(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("DEEPSEEK_API_KEY", "ds-key")
	if c := selectProvider(); c != nil {
		t.Errorf("selectProvider with only DEEPSEEK_API_KEY: want nil (retired), got %+v", c)
	}
}

// TestSelectProviderOllamaCloud verifies selection when OLLAMA_CLOUD_API_KEY
// is set and local Ollama is unreachable.
func TestSelectProviderOllamaCloud(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "oc-key")
	c := selectProvider()
	if c == nil {
		t.Fatal("selectProvider: want non-nil, got nil")
	}
	if c.name != "ollama-cloud" {
		t.Errorf("provider: want ollama-cloud, got %s", c.name)
	}
	if c.flag != "openai" {
		t.Errorf("flag: want openai (ollama cloud via openai-compat), got %s", c.flag)
	}
	if c.baseURL != ollamaCloudBaseURL {
		t.Errorf("baseURL: want %s, got %s", ollamaCloudBaseURL, c.baseURL)
	}
	if c.model != defaultOllamaCloudModel {
		t.Errorf("model: want %s, got %s", defaultOllamaCloudModel, c.model)
	}
	if c.envKey != "OPENAI_API_KEY" || c.envVal != "oc-key" {
		t.Errorf("env: want OPENAI_API_KEY=oc-key, got %s=%s", c.envKey, c.envVal)
	}
}

// TestSelectProviderOllamaCloudModelOverride verifies CLAWTA_OLLAMA_CLOUD_MODEL
// overrides the default model choice.
func TestSelectProviderOllamaCloudModelOverride(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "oc-key")
	t.Setenv("CLAWTA_OLLAMA_CLOUD_MODEL", "qwen3-coder:480b")
	c := selectProvider()
	if c == nil {
		t.Fatal("selectProvider: want non-nil, got nil")
	}
	if c.model != "qwen3-coder:480b" {
		t.Errorf("model: want qwen3-coder:480b, got %s", c.model)
	}
}

func TestSelectProviderNoneAvailable(t *testing.T) {
	isolateProviderEnv(t)
	if c := selectProvider(); c != nil {
		t.Errorf("selectProvider with no env: want nil, got %+v", c)
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

func TestDetectDefaultBranchMaster(t *testing.T) {
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
	mustGit(t, localDir, "branch", "-M", "master")
	mustGit(t, localDir, "push", "origin", "master")

	got := detectDefaultBranch(localDir)
	if got != "master" {
		t.Errorf("detectDefaultBranch: want master, got %s", got)
	}
}

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
	mustGit(t, localDir, "branch", "-M", "main")
	mustGit(t, localDir, "push", "origin", "main")

	got := detectDefaultBranch(localDir)
	if got != "main" {
		t.Errorf("detectDefaultBranch: want main, got %s", got)
	}
}

// ---- Dispatch (mocked subprocess) ----

func TestClawtaAdapterDispatchFailsGracefully(t *testing.T) {
	isolateProviderEnv(t)
	t.Setenv("OLLAMA_CLOUD_API_KEY", "test-key")

	ws := t.TempDir()
	a := NewClawtaAdapter("clawta-does-not-exist", "", "", ws)

	task := &Task{
		ID:     "test-task-001",
		Type:   "code-gen",
		Repo:   "chitinhq/octi",
		Prompt: "Add a hello world function",
	}

	result, err := a.Dispatch(context.Background(), task)
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

// TestClawtaAdapterDispatchNoProvider verifies Dispatch fails cleanly with a
// descriptive error when no inference provider is available.
func TestClawtaAdapterDispatchNoProvider(t *testing.T) {
	isolateProviderEnv(t)
	ws := t.TempDir()
	a := NewClawtaAdapter("clawta-does-not-exist", "", "", ws)
	task := &Task{ID: "noprov", Type: "code-gen", Repo: "chitinhq/octi", Prompt: "x"}
	result, err := a.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("Dispatch err: %v", err)
	}
	if result.Status != "failed" {
		t.Errorf("Status: want failed, got %s", result.Status)
	}
	if result.Error == "" {
		t.Error("Error: want non-empty explanation, got empty")
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
