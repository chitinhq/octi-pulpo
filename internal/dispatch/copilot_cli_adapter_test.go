package dispatch

import (
	"os"
	"testing"
)

func TestCopilotCLIAdapterName(t *testing.T) {
	a := NewCopilotCLIAdapter("", "")
	if got := a.Name(); got != "copilot-cli" {
		t.Errorf("Name() = %q, want %q", got, "copilot-cli")
	}
}

func TestCopilotCLIAdapterCanAccept(t *testing.T) {
	a := NewCopilotCLIAdapter("", "")

	accepted := []string{"code-gen", "bugfix", "qa", "plan", "groom", "validate", "pr-review"}
	for _, tt := range accepted {
		task := &Task{Type: tt}
		if !a.CanAccept(task) {
			t.Errorf("CanAccept(%q) = false, want true", tt)
		}
	}

	rejected := []string{"triage", "config", "evolve", "unknown"}
	for _, tt := range rejected {
		task := &Task{Type: tt}
		if a.CanAccept(task) {
			t.Errorf("CanAccept(%q) = true, want false", tt)
		}
	}
}

func TestCopilotCLIAdapterMaxTurnsForType(t *testing.T) {
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
		{"unknown", 30},
	}
	for _, tc := range cases {
		if got := maxTurnsForType(tc.taskType); got != tc.want {
			t.Errorf("maxTurnsForType(%q) = %d, want %d", tc.taskType, got, tc.want)
		}
	}
}

func TestCopilotCLIAdapterBuildArgs(t *testing.T) {
	a := NewCopilotCLIAdapter("", "")

	t.Run("low complexity code-gen", func(t *testing.T) {
		task := &Task{
			ID:      "test-123",
			Type:    "code-gen",
			Prompt:  "add a health endpoint",
			Context: "low",
		}
		args := a.buildArgs(task, "/tmp/nonexistent-repo")

		// Verify required flags are present.
		checkArgPair(t, args, "-p", "add a health endpoint")
		checkArgPair(t, args, "--model", "gpt-5.4-nano")
		checkArgPair(t, args, "--output-format", "json")
		checkArgPair(t, args, "--max-autopilot-continues", "80")
		checkArgFlag(t, args, "--yolo")
		checkArgFlag(t, args, "--no-ask-user")
		checkArgFlag(t, args, "--silent")
	})

	t.Run("high complexity plan uses gpt-5.4 and 20 turns", func(t *testing.T) {
		task := &Task{
			ID:      "plan-456",
			Type:    "plan",
			Prompt:  "design the scheduler",
			Context: "high",
		}
		args := a.buildArgs(task, "/tmp/nonexistent-repo")

		checkArgPair(t, args, "--model", "gpt-5.4")
		checkArgPair(t, args, "--max-autopilot-continues", "20")
	})

	t.Run("med complexity groom uses gpt-5.4-mini and 30 turns", func(t *testing.T) {
		task := &Task{
			ID:      "groom-789",
			Type:    "groom",
			Prompt:  "triage backlog",
			Context: "med",
		}
		args := a.buildArgs(task, "/tmp/nonexistent-repo")

		checkArgPair(t, args, "--model", "gpt-5.4-mini")
		checkArgPair(t, args, "--max-autopilot-continues", "30")
	})

	t.Run("mcp config appended when file exists", func(t *testing.T) {
		// Create a temp dir with mcp-swarm.json.
		dir := t.TempDir()
		mcpFile := dir + "/mcp-swarm.json"
		if err := writeFile(mcpFile, `{}`); err != nil {
			t.Fatal(err)
		}

		task := &Task{Type: "code-gen", Prompt: "test", Context: "low"}
		args := a.buildArgs(task, dir)

		checkArgPair(t, args, "--additional-mcp-config", "@"+mcpFile)
	})

	t.Run("no mcp config when file absent", func(t *testing.T) {
		task := &Task{Type: "code-gen", Prompt: "test", Context: "low"}
		args := a.buildArgs(task, "/tmp/no-such-dir")

		for i, arg := range args {
			if arg == "--additional-mcp-config" {
				t.Errorf("unexpected --additional-mcp-config at index %d", i)
			}
		}
	})
}

// ---- helpers ----

func checkArgPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i, arg := range args {
		if arg == flag && i+1 < len(args) && args[i+1] == value {
			return
		}
	}
	t.Errorf("args %v: missing %s %s", args, flag, value)
}

func checkArgFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, arg := range args {
		if arg == flag {
			return
		}
	}
	t.Errorf("args %v: missing flag %s", args, flag)
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
