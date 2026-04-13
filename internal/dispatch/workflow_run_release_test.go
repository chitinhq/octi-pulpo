package dispatch

import (
	"context"
	"testing"
)

// TestIsTerminalWorkflowConclusion covers the regression from chitinhq/octi#200:
// the claim release must fire on every terminal conclusion — the original code
// effectively only released on success/failure, leaking claims when a run ended
// with `skipped`.
func TestIsTerminalWorkflowConclusion(t *testing.T) {
	terminal := []string{
		"success", "failure", "cancelled", "skipped", "timed_out",
		"action_required", "stale", "neutral",
		"SKIPPED", "  success ", "Cancelled",
	}
	for _, c := range terminal {
		if !IsTerminalWorkflowConclusion(c) {
			t.Errorf("expected %q to be terminal", c)
		}
	}

	nonTerminal := []string{"", "queued", "in_progress", "waiting", "unknown"}
	for _, c := range nonTerminal {
		if IsTerminalWorkflowConclusion(c) {
			t.Errorf("expected %q to be non-terminal", c)
		}
	}
}

func TestAgentNameFromWorkflowName(t *testing.T) {
	cases := map[string]string{
		"Workspace PR Review Agent": "workspace-pr-review-agent",
		"workspace-pr-review-agent": "workspace-pr-review-agent",
		"  Claude Code  ":           "claude-code",
		"":                          "",
		"foo/bar baz":               "foo-bar-baz",
	}
	for in, want := range cases {
		if got := AgentNameFromWorkflowName(in); got != want {
			t.Errorf("AgentNameFromWorkflowName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWorkflowRunCompleted_SkippedReleasesClaim is the regression test for
// chitinhq/octi#200. Reproduces the leak (claim held after skipped run under
// the old behavior) and proves the fix (skipped → claim released).
func TestWorkflowRunCompleted_SkippedReleasesClaim(t *testing.T) {
	d, ctx := testSetup(t)
	ws := &WebhookServer{dispatcher: d}

	const agent = "workspace-pr-review-agent"

	// Simulate a prior gh-actions dispatch that set the per-agent claim.
	if _, err := d.coord.ClaimTask(ctx, agent, "event:pr_opened", 900); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	// Sanity: claim exists.
	claimKey := d.key("claim:" + agent)
	if exists, _ := d.rdb.Exists(ctx, claimKey).Result(); exists != 1 {
		t.Fatalf("precondition: expected claim key present, got exists=%d", exists)
	}

	// Simulate the GitHub workflow_run completed webhook payload with
	// conclusion=skipped — the previously-leaking case.
	payload := map[string]interface{}{
		"workflow_run": map[string]interface{}{
			"name":       "Workspace PR Review Agent",
			"conclusion": "skipped",
			"status":     "completed",
		},
		"workflow": map[string]interface{}{
			"name": "Workspace PR Review Agent",
		},
	}

	released, gotAgent, gotConclusion := ws.HandleWorkflowRunCompleted(context.Background(), payload)
	if !released {
		t.Fatalf("expected claim released on skipped conclusion, got released=false (agent=%q conclusion=%q)", gotAgent, gotConclusion)
	}
	if gotAgent != agent {
		t.Errorf("agent = %q, want %q", gotAgent, agent)
	}
	if gotConclusion != "skipped" {
		t.Errorf("conclusion = %q, want skipped", gotConclusion)
	}

	if exists, _ := d.rdb.Exists(ctx, claimKey).Result(); exists != 0 {
		t.Fatalf("expected claim key deleted after skipped workflow_run, got exists=%d", exists)
	}
}

// TestWorkflowRunCompleted_AllTerminalConclusionsRelease verifies every terminal
// conclusion triggers a release. Pre-fix, success and failure may have been the
// only conclusions handled; now all eight must release the claim.
func TestWorkflowRunCompleted_AllTerminalConclusionsRelease(t *testing.T) {
	d, ctx := testSetup(t)
	ws := &WebhookServer{dispatcher: d}

	const agent = "workspace-pr-review-agent"
	claimKey := d.key("claim:" + agent)

	for _, conclusion := range []string{
		"success", "failure", "cancelled", "skipped",
		"timed_out", "action_required", "stale", "neutral",
	} {
		if _, err := d.coord.ClaimTask(ctx, agent, "event:test", 900); err != nil {
			t.Fatalf("seed claim: %v", err)
		}

		payload := map[string]interface{}{
			"workflow_run": map[string]interface{}{
				"name":       "workspace-pr-review-agent",
				"conclusion": conclusion,
				"status":     "completed",
			},
		}

		released, _, _ := ws.HandleWorkflowRunCompleted(context.Background(), payload)
		if !released {
			t.Errorf("conclusion=%q: expected released=true", conclusion)
		}
		if exists, _ := d.rdb.Exists(ctx, claimKey).Result(); exists != 0 {
			t.Errorf("conclusion=%q: claim not deleted", conclusion)
		}
	}
}

// TestWorkflowRunCompleted_NonTerminalIgnored ensures we do not prematurely
// release claims on in-progress / queued workflow_run events.
func TestWorkflowRunCompleted_NonTerminalIgnored(t *testing.T) {
	d, ctx := testSetup(t)
	ws := &WebhookServer{dispatcher: d}

	const agent = "workspace-pr-review-agent"
	if _, err := d.coord.ClaimTask(ctx, agent, "event:test", 900); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	claimKey := d.key("claim:" + agent)

	payload := map[string]interface{}{
		"workflow_run": map[string]interface{}{
			"name":       "workspace-pr-review-agent",
			"conclusion": "in_progress",
			"status":     "in_progress",
		},
	}
	released, _, _ := ws.HandleWorkflowRunCompleted(context.Background(), payload)
	if released {
		t.Fatalf("expected released=false for non-terminal conclusion")
	}
	if exists, _ := d.rdb.Exists(ctx, claimKey).Result(); exists != 1 {
		t.Fatalf("expected claim still held, got exists=%d", exists)
	}
}
