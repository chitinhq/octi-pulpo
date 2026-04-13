package dispatch

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// terminalWorkflowConclusions enumerates every GitHub Actions `workflow_run`
// conclusion that signals the run has reached a terminal state. Any of these
// must trigger a claim release — historically we only released on
// "success"/"failure", which leaked claims when workflows short-circuited with
// `skipped` (see chitinhq/octi#200).
var terminalWorkflowConclusions = map[string]struct{}{
	"success":         {},
	"failure":         {},
	"cancelled":       {},
	"skipped":         {},
	"timed_out":       {},
	"action_required": {},
	"stale":           {},
	"neutral":         {},
}

// IsTerminalWorkflowConclusion reports whether a GitHub Actions
// workflow_run.conclusion value indicates the run is finished and its
// per-agent claim should be released.
func IsTerminalWorkflowConclusion(conclusion string) bool {
	_, ok := terminalWorkflowConclusions[strings.ToLower(strings.TrimSpace(conclusion))]
	return ok
}

// AgentNameFromWorkflowName derives the dispatch agent name from a GitHub
// Actions workflow name. Agent names are kebab-cased and match the workflow
// file's display name (e.g. "Workspace PR Review Agent" →
// "workspace-pr-review-agent"). The helper is forgiving of casing and
// surrounding whitespace.
func AgentNameFromWorkflowName(workflowName string) string {
	name := strings.ToLower(strings.TrimSpace(workflowName))
	if name == "" {
		return ""
	}
	// Collapse whitespace runs and slashes into single hyphens.
	var b strings.Builder
	prevDash := false
	for _, r := range name {
		switch {
		case r == ' ' || r == '_' || r == '/' || r == '\t':
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		case r == '-':
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		default:
			b.WriteRune(r)
			prevDash = false
		}
	}
	return strings.Trim(b.String(), "-")
}

// HandleWorkflowRunCompleted releases the per-agent claim when a workflow_run
// webhook fires with action=completed, regardless of conclusion. This closes
// chitinhq/octi#200 — previously claims only cleared via TTL when a run
// concluded with anything other than success/failure (most notably
// `skipped`, which is the common case for no-op Claude Code runs).
func (ws *WebhookServer) HandleWorkflowRunCompleted(ctx context.Context, payload map[string]interface{}) (released bool, agentName string, conclusion string) {
	workflowRun, ok := payload["workflow_run"].(map[string]interface{})
	if !ok {
		return false, "", ""
	}
	conclusion = getString(workflowRun, "conclusion")
	if !IsTerminalWorkflowConclusion(conclusion) {
		return false, "", conclusion
	}

	// Prefer the workflow's name (stable display name), fall back to the
	// workflow_run name field. Both appear in the GitHub webhook payload.
	workflowName := getNestedString(payload, "workflow", "name")
	if workflowName == "" {
		workflowName = getString(workflowRun, "name")
	}
	agentName = AgentNameFromWorkflowName(workflowName)
	if agentName == "" || ws.dispatcher == nil {
		return false, agentName, conclusion
	}

	if err := ws.dispatcher.ReleaseClaim(ctx, agentName); err != nil {
		fmt.Fprintf(os.Stderr, "[octi-pulpo] workflow_run release claim %s (conclusion=%s) failed: %v\n",
			agentName, conclusion, err)
		return false, agentName, conclusion
	}
	fmt.Fprintf(os.Stderr, "[octi-pulpo] workflow_run released claim agent=%s conclusion=%s\n",
		agentName, conclusion)
	return true, agentName, conclusion
}
