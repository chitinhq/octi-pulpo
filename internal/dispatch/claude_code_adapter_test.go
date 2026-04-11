package dispatch

import (
	"os"
	"testing"
)

func TestClaudeCodeAdapterName(t *testing.T) {
	a := NewClaudeCodeAdapter("", "")
	if got := a.Name(); got != "claude-code" {
		t.Errorf("Name() = %q, want %q", got, "claude-code")
	}
}

func TestClaudeCodeAdapterCanAccept(t *testing.T) {
	// Ensure the key is set so CanAccept doesn't short-circuit.
	os.Setenv("ANTHROPIC_API_KEY", "test-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	a := NewClaudeCodeAdapter("", "")

	accepted := []string{"code-gen", "bugfix", "qa", "plan", "groom", "validate", "pr-review", "triage"}
	for _, tt := range accepted {
		task := &Task{Type: tt}
		if !a.CanAccept(task) {
			t.Errorf("CanAccept(%q) = false, want true", tt)
		}
	}

	rejected := []string{"config", "evolve", "deploy", "unknown"}
	for _, tt := range rejected {
		task := &Task{Type: tt}
		if a.CanAccept(task) {
			t.Errorf("CanAccept(%q) = true, want false", tt)
		}
	}
}

func TestClaudeCodeAdapterCanAcceptNoKey(t *testing.T) {
	// Claude Code CLI uses Max plan OAuth — no API key required.
	os.Unsetenv("ANTHROPIC_API_KEY")
	a := NewClaudeCodeAdapter("", "")
	task := &Task{Type: "code-gen"}
	if !a.CanAccept(task) {
		t.Error("CanAccept should return true even without ANTHROPIC_API_KEY (Max plan OAuth)")
	}
}

func TestMaxTurnsForType(t *testing.T) {
	cases := []struct {
		taskType string
		want     int
	}{
		{"groom", 30},
		{"plan", 20},
		{"validate", 30},
		{"qa", 30},
		{"pr-review", 30},
		{"code-gen", 80},
		{"bugfix", 80},
		{"triage", 30},   // default
		{"unknown", 30},  // default
	}
	for _, tc := range cases {
		got := maxTurnsForType(tc.taskType)
		if got != tc.want {
			t.Errorf("maxTurnsForType(%q) = %d, want %d", tc.taskType, got, tc.want)
		}
	}
}

func TestClaudeCodeAdapterBuildArgs(t *testing.T) {
	a := NewClaudeCodeAdapter("claude", "/tmp/workspace")

	t.Run("low complexity code-gen", func(t *testing.T) {
		task := &Task{
			Type:    "code-gen",
			Prompt:  "implement foo",
			Context: "low",
		}
		// Use a temp dir that has no mcp-swarm.json
		worktreePath := t.TempDir()
		args := a.buildArgs(task, worktreePath)

		assertArg(t, args, "-p", "implement foo")
		assertArg(t, args, "--model", "sonnet")
		assertFlag(t, args, "--dangerously-skip-permissions")
		assertArg(t, args, "--max-turns", "80")
		assertArg(t, args, "--output-format", "json")
		assertAbsent(t, args, "--mcp-config")
	})

	t.Run("high complexity plan", func(t *testing.T) {
		task := &Task{
			Type:    "plan",
			Prompt:  "design the system",
			Context: "high",
		}
		worktreePath := t.TempDir()
		args := a.buildArgs(task, worktreePath)

		assertArg(t, args, "--model", "opus")
		assertArg(t, args, "--max-turns", "20")
	})

	t.Run("mcp-swarm.json present", func(t *testing.T) {
		task := &Task{
			Type:    "bugfix",
			Prompt:  "fix the thing",
			Context: "med",
		}
		worktreePath := t.TempDir()
		// Create a dummy mcp-swarm.json
		mcpPath := worktreePath + "/mcp-swarm.json"
		if err := os.WriteFile(mcpPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		args := a.buildArgs(task, worktreePath)
		assertArg(t, args, "--mcp-config", mcpPath)
	})

	t.Run("groom task turns", func(t *testing.T) {
		task := &Task{Type: "groom", Prompt: "clean up backlog", Context: "low"}
		args := a.buildArgs(task, t.TempDir())
		assertArg(t, args, "--max-turns", "30")
	})

	t.Run("validate task turns", func(t *testing.T) {
		task := &Task{Type: "validate", Prompt: "check invariants", Context: "med"}
		args := a.buildArgs(task, t.TempDir())
		assertArg(t, args, "--max-turns", "30")
	})
}

// assertArg checks that flag appears immediately followed by value in args.
func assertFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			return
		}
	}
	t.Errorf("flag %q not found in args %v", flag, args)
}

func assertArg(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i, a := range args {
		if a == flag {
			if i+1 >= len(args) {
				t.Errorf("flag %q present but no following value", flag)
				return
			}
			if args[i+1] != value {
				t.Errorf("flag %q = %q, want %q", flag, args[i+1], value)
			}
			return
		}
	}
	t.Errorf("flag %q not found in args %v", flag, args)
}

// assertAbsent checks that flag does NOT appear in args.
func assertAbsent(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			t.Errorf("flag %q should not be present in args %v", flag, args)
			return
		}
	}
}
