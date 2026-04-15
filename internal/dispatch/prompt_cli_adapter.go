package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// DefaultPromptCLITimeout is the fallback timeout for a freeform prompt.
const DefaultPromptCLITimeout = 120 * time.Second

// PromptCLIRunner executes a CLI subprocess and returns its combined stdout.
// The prompt is passed via argv; this is safe because exec.Command does not
// spawn a shell, so no shell interpolation/injection is possible.
// Injected in tests to exercise selection/timeout/fallback logic without
// real subprocesses.
type PromptCLIRunner func(ctx context.Context, driver, binary, systemPrompt, prompt string) (stdout []byte, err error)

// AllowedDrivers is the allowlist of accepted driver names. Unknown values
// are rejected at the boundary to prevent arbitrary-binary execution via a
// user-controlled `preferred_driver` falling through to a default case.
//
// Ladder Forge II (2026-04-14): CLI drivers (copilot, codex, claude-code)
// pruned. Openclaw is the only remaining local-CLI surface.
var AllowedDrivers = map[string]bool{
	"openclaw": true,
	"":         true, // empty → use default fallback chain
}

// ValidatePreferredDriver returns an error if driver is not on the allowlist.
func ValidatePreferredDriver(driver string) error {
	if !AllowedDrivers[driver] {
		return fmt.Errorf("invalid preferred_driver %q: allowed values are openclaw or empty", driver)
	}
	return nil
}

// PromptCLIRequest is a freeform prompt-to-CLI dispatch input.
type PromptCLIRequest struct {
	Prompt          string
	SystemPrompt    string
	PreferredDriver string        // "openclaw" | ""
	Timeout         time.Duration // 0 → DefaultPromptCLITimeout
}

// PromptCLIResult mirrors the MCP tool's output shape.
type PromptCLIResult struct {
	DriverUsed string `json:"driver_used"`
	Output     string `json:"output"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// PromptCLIAdapter dispatches a freeform prompt to a local CLI agent
// (openclaw) without requiring a git worktree or an API key. It picks a
// driver by simple fallback policy and executes one subprocess, passing
// the prompt via argv (safe under exec.Command, which does not spawn a
// shell). Ladder Forge II pruned copilot/codex/claude-code CLI drivers.
type PromptCLIAdapter struct {
	// DriverOrder overrides the default fallback chain when non-empty.
	DriverOrder []string
	// Binaries maps driver name → binary path. Zero-values fall back to
	// each driver's canonical name on PATH.
	Binaries map[string]string
	// Runner is injected in tests. nil → real exec.
	Runner PromptCLIRunner
	// LookPath is injected in tests to simulate missing CLIs. nil → exec.LookPath.
	LookPath func(string) (string, error)
}

// DefaultDriverOrder is the fallback chain when PreferredDriver is unset.
// Ladder Forge II (2026-04-14): CLI drivers pruned; openclaw is sole remainder.
var DefaultDriverOrder = []string{"openclaw"}

// NewPromptCLIAdapter returns an adapter with defaults wired in.
func NewPromptCLIAdapter() *PromptCLIAdapter {
	return &PromptCLIAdapter{
		DriverOrder: append([]string(nil), DefaultDriverOrder...),
		Binaries: map[string]string{
			"openclaw": "openclaw",
		},
	}
}

// Name identifies this adapter.
func (a *PromptCLIAdapter) Name() string { return "prompt-cli" }

// binaryFor resolves the binary path for a driver, falling back to the
// canonical name if not configured.
func (a *PromptCLIAdapter) binaryFor(driver string) string {
	if a.Binaries != nil {
		if b, ok := a.Binaries[driver]; ok && b != "" {
			return b
		}
	}
	switch driver {
	case "openclaw":
		return "openclaw"
	}
	return driver
}

// driversToTry builds the ordered candidate list honoring a preference.
func (a *PromptCLIAdapter) driversToTry(preferred string) []string {
	order := a.DriverOrder
	if len(order) == 0 {
		order = DefaultDriverOrder
	}
	if preferred == "" {
		return append([]string(nil), order...)
	}
	// Put preferred first, then the remainder of the chain as fallback.
	out := []string{preferred}
	for _, d := range order {
		if d != preferred {
			out = append(out, d)
		}
	}
	return out
}

// lookPath resolves a binary via the injected LookPath or exec.LookPath.
func (a *PromptCLIAdapter) lookPath(bin string) (string, error) {
	if a.LookPath != nil {
		return a.LookPath(bin)
	}
	return exec.LookPath(bin)
}

// Dispatch runs the freeform prompt against the first available CLI.
// It never calls a paid API and never creates a worktree.
func (a *PromptCLIAdapter) Dispatch(ctx context.Context, req *PromptCLIRequest) *PromptCLIResult {
	start := time.Now()
	res := &PromptCLIResult{DriverUsed: "none"}

	if req == nil || req.Prompt == "" {
		res.Error = "prompt is required"
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	if err := ValidatePreferredDriver(req.PreferredDriver); err != nil {
		res.Error = err.Error()
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultPromptCLITimeout
	}
	// Compute a single cumulative deadline shared across all fallback
	// candidates so that N drivers × timeout can never multiply into
	// N*timeout worst-case runtime.
	deadline := time.Now().Add(timeout)

	drivers := a.driversToTry(req.PreferredDriver)

	var lastErr error
	for _, driver := range drivers {
		if time.Now().After(deadline) {
			lastErr = fmt.Errorf("timeout: cumulative deadline exceeded before trying %s", driver)
			break
		}
		bin := a.binaryFor(driver)
		if _, err := a.lookPath(bin); err != nil {
			lastErr = fmt.Errorf("%s: not installed (%s)", driver, bin)
			continue
		}

		cctx, cancel := context.WithDeadline(ctx, deadline)
		runner := a.Runner
		if runner == nil {
			runner = realPromptCLIRunner
		}
		out, err := runner(cctx, driver, bin, req.SystemPrompt, req.Prompt)
		cancel()

		if err != nil {
			lastErr = fmt.Errorf("%s: %w", driver, err)
			// If the shared deadline fired, stop trying further drivers.
			if cctx.Err() == context.DeadlineExceeded || time.Now().After(deadline) {
				break
			}
			// Treat transient errors as a reason to fall back.
			continue
		}

		res.DriverUsed = driver
		res.Output = string(out)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	if lastErr == nil {
		lastErr = errors.New("no CLI driver available")
	}
	res.Error = lastErr.Error()
	res.DurationMS = time.Since(start).Milliseconds()
	return res
}

// realPromptCLIRunner invokes the real CLI subprocess. The prompt is
// passed via argv; this is safe because exec.Command does not spawn a
// shell, so there is no shell interpolation. Driver names are validated
// against AllowedDrivers at the Dispatch boundary so buildPromptCLIArgs
// will never receive an unknown driver here.
func realPromptCLIRunner(ctx context.Context, driver, binary, systemPrompt, prompt string) ([]byte, error) {
	if !AllowedDrivers[driver] || driver == "" {
		return nil, fmt.Errorf("refusing to exec unknown driver %q", driver)
	}
	args, stdinPayload := buildPromptCLIArgs(driver, systemPrompt, prompt)
	if len(args) == 0 {
		return nil, fmt.Errorf("refusing to exec driver %q with empty args", driver)
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewReader([]byte(stdinPayload))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.Bytes(), fmt.Errorf("timeout: %w", err)
		}
		msg := stderr.String()
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("exit: %s", msg)
	}
	return stdout.Bytes(), nil
}

// buildPromptCLIArgs returns (args, stdin) for a driver. The prompt is
// passed via argv, which is safe because exec.Command does not spawn a
// shell. Unknown drivers return (nil, "") and MUST be rejected by the
// caller — never exec'd.
func buildPromptCLIArgs(driver, systemPrompt, prompt string) ([]string, string) {
	switch driver {
	case "openclaw":
		// openclaw accepts prompt via argv; system prompt appended if present.
		args := []string{"-p", prompt}
		if systemPrompt != "" {
			args = append(args, "--append-system-prompt", systemPrompt)
		}
		return args, ""
	}
	// Unknown driver: refuse to build any args. Callers must validate
	// against AllowedDrivers before reaching here.
	return nil, ""
}
