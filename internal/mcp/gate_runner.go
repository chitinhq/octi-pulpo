package mcp

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// GateRunner runs a named chitin gate against a target ref (PR number as string,
// branch name, or empty for cwd). Returns nil on pass, error on fail. The error
// message is surfaced back through sprint_complete so the agent can retry.
//
// Implementations shell out to `chitin gate run <name>` by default; tests inject
// a mock to avoid a real chitin dependency.
type GateRunner interface {
	Run(ctx context.Context, gate string, ref string) error
}

// execGateRunner is the production GateRunner: it shells out to `chitin`.
type execGateRunner struct{}

func (execGateRunner) Run(ctx context.Context, gate string, ref string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{"gate", "run", gate}
	if ref != "" {
		args = append(args, "--ref", ref)
	}
	cmd := exec.CommandContext(ctx, "chitin", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gate %s failed: %v: %s", gate, err, truncateOutput(out, 2048))
	}
	return nil
}

// truncateOutput returns the last `max` bytes of `out`, prefixed with a marker
// when truncation occurred. Keeps JSON-RPC error payloads bounded and avoids
// leaking large env/path dumps from chatty gate scripts.
func truncateOutput(out []byte, max int) string {
	if len(out) <= max {
		return string(out)
	}
	return fmt.Sprintf("...[truncated %d bytes]...%s", len(out)-max, string(out[len(out)-max:]))
}

// SetGateRunner overrides the default gate runner. Used by tests and to wire a
// real chitin binary at startup. If never called, the shell-out implementation
// is used.
func (s *Server) SetGateRunner(gr GateRunner) { s.gateRunner = gr }

// runSprintCompleteGates executes the gate chain required to mark a sprint item
// done. Returns the first failing gate name and its error, or ("", nil) on pass.
// When prNumber == 0 the ref is empty (gates run against cwd / default).
func (s *Server) runSprintCompleteGates(ctx context.Context, repo string, prNumber int) (string, error) {
	if s.gateRunner == nil {
		s.gateRunner = execGateRunner{}
	}
	ref := ""
	if prNumber > 0 {
		ref = fmt.Sprintf("%s#%d", repo, prNumber)
	}
	gates := []string{"validate/check_ci_passed"}
	for _, g := range gates {
		if err := s.gateRunner.Run(ctx, g, ref); err != nil {
			return g, err
		}
	}
	return "", nil
}
