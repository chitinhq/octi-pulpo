package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// newTestAdapter builds an adapter with an injected LookPath that treats
// any driver in `available` as installed. The runner is customizable.
func newTestAdapter(available map[string]bool, runner PromptCLIRunner) *PromptCLIAdapter {
	a := NewPromptCLIAdapter()
	a.LookPath = func(bin string) (string, error) {
		// Map binary name → driver via default table.
		driver := ""
		switch bin {
		case "copilot":
			driver = "copilot"
		case "codex":
			driver = "codex"
		case "claude":
			driver = "claude-code"
		}
		if available[driver] {
			return "/usr/bin/" + bin, nil
		}
		return "", errors.New("not found")
	}
	a.Runner = runner
	return a
}

func TestPromptCLI_DriverSelection_PrefersRequested(t *testing.T) {
	var called string
	runner := func(ctx context.Context, driver, binary, system, prompt string) ([]byte, error) {
		called = driver
		return []byte("ok"), nil
	}
	a := newTestAdapter(map[string]bool{"copilot": true, "codex": true, "claude-code": true}, runner)

	res := a.Dispatch(context.Background(), &PromptCLIRequest{
		Prompt:          "hello",
		PreferredDriver: "codex",
	})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if called != "codex" {
		t.Fatalf("expected codex, got %q", called)
	}
	if res.DriverUsed != "codex" {
		t.Fatalf("result driver = %q", res.DriverUsed)
	}
	if res.Output != "ok" {
		t.Fatalf("result output = %q", res.Output)
	}
}

func TestPromptCLI_FallbackOrder(t *testing.T) {
	// Only claude-code installed — adapter must skip copilot & codex.
	var called []string
	runner := func(ctx context.Context, driver, binary, system, prompt string) ([]byte, error) {
		called = append(called, driver)
		return []byte("claude output"), nil
	}
	a := newTestAdapter(map[string]bool{"claude-code": true}, runner)

	res := a.Dispatch(context.Background(), &PromptCLIRequest{Prompt: "hi"})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.DriverUsed != "claude-code" {
		t.Fatalf("expected claude-code, got %q", res.DriverUsed)
	}
	if len(called) != 1 || called[0] != "claude-code" {
		t.Fatalf("expected single claude-code invocation, got %v", called)
	}
}

func TestPromptCLI_AllMissing(t *testing.T) {
	runner := func(ctx context.Context, driver, binary, system, prompt string) ([]byte, error) {
		t.Fatalf("runner should not be called when no CLIs installed")
		return nil, nil
	}
	a := newTestAdapter(map[string]bool{}, runner)

	res := a.Dispatch(context.Background(), &PromptCLIRequest{Prompt: "hi"})
	if res.Error == "" {
		t.Fatal("expected error when no CLI available")
	}
	if res.DriverUsed != "none" {
		t.Fatalf("expected driver_used=none, got %q", res.DriverUsed)
	}
}

func TestPromptCLI_FallsBackOnRunnerError(t *testing.T) {
	var called []string
	runner := func(ctx context.Context, driver, binary, system, prompt string) ([]byte, error) {
		called = append(called, driver)
		if driver == "copilot" {
			return nil, errors.New("copilot crashed")
		}
		return []byte("codex worked"), nil
	}
	a := newTestAdapter(map[string]bool{"copilot": true, "codex": true}, runner)

	res := a.Dispatch(context.Background(), &PromptCLIRequest{Prompt: "hi"})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.DriverUsed != "codex" {
		t.Fatalf("expected fallback to codex, got %q", res.DriverUsed)
	}
	if len(called) != 2 || called[0] != "copilot" || called[1] != "codex" {
		t.Fatalf("expected copilot→codex, got %v", called)
	}
}

func TestPromptCLI_Timeout(t *testing.T) {
	runner := func(ctx context.Context, driver, binary, system, prompt string) ([]byte, error) {
		// Block until the injected timeout fires.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return []byte("should not happen"), nil
		}
	}
	a := newTestAdapter(map[string]bool{"copilot": true}, runner)

	start := time.Now()
	res := a.Dispatch(context.Background(), &PromptCLIRequest{
		Prompt:  "hi",
		Timeout: 50 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("timeout not enforced, elapsed=%s", elapsed)
	}
	if res.Error == "" {
		t.Fatal("expected error from timeout")
	}
	if res.DriverUsed != "none" {
		t.Fatalf("expected driver_used=none on timeout, got %q", res.DriverUsed)
	}
}

func TestPromptCLI_EmptyPrompt(t *testing.T) {
	a := newTestAdapter(map[string]bool{"copilot": true}, nil)
	res := a.Dispatch(context.Background(), &PromptCLIRequest{})
	if res.Error == "" {
		t.Fatal("expected error for empty prompt")
	}
}

func TestBuildPromptCLIArgs(t *testing.T) {
	cases := []struct {
		driver    string
		prompt    string
		system    string
		mustHave  []string
	}{
		{"copilot", "do thing", "", []string{"-p", "do thing", "--allow-all-tools"}},
		{"copilot", "do thing", "be terse", []string{"--append-system-prompt", "be terse"}},
		{"codex", "do thing", "", []string{"exec", "do thing"}},
		{"claude-code", "do thing", "", []string{"-p", "do thing"}},
	}
	for _, tc := range cases {
		args, _ := buildPromptCLIArgs(tc.driver, tc.system, tc.prompt)
		joined := strings.Join(args, "|")
		for _, want := range tc.mustHave {
			if !strings.Contains(joined, want) {
				t.Errorf("driver=%s args=%v missing %q", tc.driver, args, want)
			}
		}
	}
}
