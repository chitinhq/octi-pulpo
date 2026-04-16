package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockGateRunner records calls and returns a scripted error per gate name.
type mockGateRunner struct {
	errs  map[string]error
	calls []string
}

func (m *mockGateRunner) Run(ctx context.Context, gate, ref string) error {
	m.calls = append(m.calls, gate+"@"+ref)
	if err, ok := m.errs[gate]; ok {
		return err
	}
	return nil
}

func TestRunSprintCompleteGates_CIFailBlocks(t *testing.T) {
	s := &Server{}
	m := &mockGateRunner{errs: map[string]error{
		"validate/check_ci_passed": errors.New("CI red on last commit"),
	}}
	s.SetGateRunner(m)

	failed, err := s.runSprintCompleteGates(context.Background(), "chitinhq/octi", 42)
	if err == nil {
		t.Fatal("expected error when CI gate fails, got nil")
	}
	if failed != "validate/check_ci_passed" {
		t.Errorf("failed gate name: got %q, want validate/check_ci_passed", failed)
	}
	if !strings.Contains(err.Error(), "CI red") {
		t.Errorf("error should surface underlying cause, got: %v", err)
	}
	if len(m.calls) != 1 {
		t.Errorf("expected 1 gate call, got %d: %v", len(m.calls), m.calls)
	}
	if !strings.Contains(m.calls[0], "chitinhq/octi#42") {
		t.Errorf("gate should receive repo#PR ref, got: %s", m.calls[0])
	}
}

func TestRunSprintCompleteGates_CIGreenPasses(t *testing.T) {
	s := &Server{}
	m := &mockGateRunner{errs: map[string]error{}}
	s.SetGateRunner(m)

	failed, err := s.runSprintCompleteGates(context.Background(), "chitinhq/octi", 99)
	if err != nil {
		t.Fatalf("expected nil error on green CI, got: %v", err)
	}
	if failed != "" {
		t.Errorf("expected empty failed gate, got %q", failed)
	}
}

func TestRunSprintCompleteGates_NoPRNumberEmptyRef(t *testing.T) {
	s := &Server{}
	m := &mockGateRunner{}
	s.SetGateRunner(m)

	_, err := s.runSprintCompleteGates(context.Background(), "chitinhq/octi", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.calls) != 1 || !strings.HasSuffix(m.calls[0], "@") {
		t.Errorf("expected empty ref when no PR, got: %v", m.calls)
	}
}
