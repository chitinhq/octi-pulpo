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
		// Ladder Forge II (2026-04-14): openclaw is the sole surviving CLI driver.
		driver := ""
		switch bin {
		case "openclaw":
			driver = "openclaw"
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
	a := newTestAdapter(map[string]bool{"openclaw": true}, runner)

	res := a.Dispatch(context.Background(), &PromptCLIRequest{
		Prompt:          "hello",
		PreferredDriver: "openclaw",
	})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if called != "openclaw" {
		t.Fatalf("expected openclaw, got %q", called)
	}
	if res.DriverUsed != "openclaw" {
		t.Fatalf("result driver = %q", res.DriverUsed)
	}
	if res.Output != "ok" {
		t.Fatalf("result output = %q", res.Output)
	}
}

func TestPromptCLI_FallbackOrder(t *testing.T) {
	// Ladder Forge II (2026-04-14): CLI fallback chain collapsed to single
	// openclaw driver. Test verifies it's invoked when present.
	var called []string
	runner := func(ctx context.Context, driver, binary, system, prompt string) ([]byte, error) {
		called = append(called, driver)
		return []byte("openclaw output"), nil
	}
	a := newTestAdapter(map[string]bool{"openclaw": true}, runner)

	res := a.Dispatch(context.Background(), &PromptCLIRequest{Prompt: "hi"})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.DriverUsed != "openclaw" {
		t.Fatalf("expected openclaw, got %q", res.DriverUsed)
	}
	if len(called) != 1 || called[0] != "openclaw" {
		t.Fatalf("expected single openclaw invocation, got %v", called)
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
	// Ladder Forge II (2026-04-14): single-driver chain (openclaw). When it
	// errors, dispatch returns the error — no fallback candidates remain.
	var called []string
	runner := func(ctx context.Context, driver, binary, system, prompt string) ([]byte, error) {
		called = append(called, driver)
		return nil, errors.New("openclaw crashed")
	}
	a := newTestAdapter(map[string]bool{"openclaw": true}, runner)

	res := a.Dispatch(context.Background(), &PromptCLIRequest{Prompt: "hi"})
	if res.Error == "" {
		t.Fatal("expected error when sole driver fails")
	}
	if len(called) != 1 || called[0] != "openclaw" {
		t.Fatalf("expected single openclaw attempt, got %v", called)
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
	a := newTestAdapter(map[string]bool{"openclaw": true}, runner)

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
	a := newTestAdapter(map[string]bool{"openclaw": true}, nil)
	res := a.Dispatch(context.Background(), &PromptCLIRequest{})
	if res.Error == "" {
		t.Fatal("expected error for empty prompt")
	}
}

func TestPromptCLI_RejectsUnknownPreferredDriver(t *testing.T) {
	// Arbitrary binary execution guard: unknown driver must be rejected
	// at the Dispatch boundary without invoking the runner.
	runnerCalled := false
	runner := func(ctx context.Context, driver, binary, system, prompt string) ([]byte, error) {
		runnerCalled = true
		return nil, nil
	}
	a := newTestAdapter(map[string]bool{"openclaw": true}, runner)
	res := a.Dispatch(context.Background(), &PromptCLIRequest{
		Prompt:          "hi",
		PreferredDriver: "/bin/sh",
	})
	if res.Error == "" {
		t.Fatal("expected rejection error for unknown preferred_driver")
	}
	if !strings.Contains(res.Error, "invalid preferred_driver") {
		t.Fatalf("expected invalid preferred_driver error, got %q", res.Error)
	}
	if runnerCalled {
		t.Fatal("runner must not be called when preferred_driver is rejected")
	}
	if res.DriverUsed != "none" {
		t.Fatalf("expected driver_used=none, got %q", res.DriverUsed)
	}
}

func TestValidatePreferredDriver(t *testing.T) {
	// Ladder Forge II (2026-04-14): openclaw is the sole remaining CLI driver.
	good := []string{"", "openclaw"}
	for _, d := range good {
		if err := ValidatePreferredDriver(d); err != nil {
			t.Errorf("expected %q allowed, got err=%v", d, err)
		}
	}
	bad := []string{"sh", "../../../bin/rm", "openclaw ", "OPENCLAW", "/bin/sh", "copilot", "codex", "claude-code"}
	for _, d := range bad {
		if err := ValidatePreferredDriver(d); err == nil {
			t.Errorf("expected %q rejected", d)
		}
	}
}

func TestPromptCLI_CumulativeDeadline(t *testing.T) {
	// With 3 fallback candidates each taking ~timeout, the overall
	// dispatch must not exceed timeout by a large factor. Regression
	// test for multiplied-timeout bug (context.WithTimeout in loop).
	var calls int
	runner := func(ctx context.Context, driver, binary, system, prompt string) ([]byte, error) {
		calls++
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return []byte("should not happen"), nil
		}
	}
	a := newTestAdapter(map[string]bool{"openclaw": true}, runner)

	start := time.Now()
	res := a.Dispatch(context.Background(), &PromptCLIRequest{
		Prompt:  "hi",
		Timeout: 100 * time.Millisecond,
	})
	elapsed := time.Since(start)

	// With a cumulative deadline, total elapsed must be ~timeout, not 3x.
	if elapsed > 800*time.Millisecond {
		t.Fatalf("cumulative deadline not enforced: elapsed=%s (expected ~100ms, must be <800ms)", elapsed)
	}
	if res.Error == "" {
		t.Fatal("expected error from cumulative timeout")
	}
	if calls > 3 {
		t.Fatalf("runner called %d times, expected ≤3", calls)
	}
}

func TestPromptCLI_UnknownDriverViaDriverOrder(t *testing.T) {
	// Even if DriverOrder is tampered with, realPromptCLIRunner must
	// refuse to exec an unknown driver. We exercise buildPromptCLIArgs
	// and the guard directly.
	args, stdin := buildPromptCLIArgs("evil-binary", "", "prompt")
	if args != nil {
		t.Fatalf("expected nil args for unknown driver, got %v", args)
	}
	if stdin != "" {
		t.Fatalf("expected empty stdin for unknown driver, got %q", stdin)
	}
}

func TestBuildPromptCLIArgs(t *testing.T) {
	cases := []struct {
		driver    string
		prompt    string
		system    string
		mustHave  []string
	}{
		{"openclaw", "do thing", "", []string{"-p", "do thing"}},
		{"openclaw", "do thing", "be terse", []string{"--append-system-prompt", "be terse"}},
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
