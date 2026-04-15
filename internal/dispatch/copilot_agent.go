package dispatch

// T2b Copilot Agent telemetry (agentguardhq Enterprise Copilot).
//
// There is no programmatic session-create API and no copilot_session.*
// webhook. Invocation happens via POST /repos/{owner}/{repo}/issues/
// {issue_number}/assignees with assignee "Copilot"; the agent later
// opens/updates a PR from actor "copilot-swe-agent[bot]". We observe
// that flow via the standard GitHub webhook events and synthesize
// dispatch-log rows so the session is counted in swarm_today's
// tiers.copilot column.
//
// Detection is a pure function over the webhook payload so it is
// replayable against fixtures. See copilot_agent_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CopilotAgentBot is the login GitHub uses for the Copilot coding agent
// when it authors PRs / commits.
const CopilotAgentBot = "copilot-swe-agent[bot]"

// CopilotAgentDriver is the driver string we persist on synthesized
// DispatchRecords. ClassifyTier and swarm_today.tierFor both map this
// to the "copilot" tier.
const CopilotAgentDriver = "copilot-agent"

// CopilotAgentKind classifies what the webhook tells us happened.
type CopilotAgentKind string

const (
	CopilotAgentDispatched CopilotAgentKind = "dispatched" // Copilot assigned to an issue
	CopilotAgentCompleted  CopilotAgentKind = "completed"  // Copilot opened/updated a PR
	CopilotAgentCanceled   CopilotAgentKind = "canceled"   // Copilot unassigned
)

// CopilotAgentEvent is the minimal record extracted from a webhook
// payload. Org/Repo/Issue/PR form the join key — there is no
// agent_session_id on webhooks (only in the Enterprise audit log),
// so we correlate dispatch → completion via (repo, issue) pairs.
type CopilotAgentEvent struct {
	Kind  CopilotAgentKind
	Org   string
	Repo  string // full_name e.g. "agentguardhq/widget"
	Issue int    // 0 if not applicable
	PR    int    // 0 if not applicable
	Actor string // who triggered (for dispatch, the assigner)
}

// DetectCopilotAgentEvent is the pure detection rule. It returns nil
// when the payload is not a Copilot-agent signal. Fixture-replayable.
//
// Rules:
//  1. issues.assigned  where assignee.login == "Copilot"            -> dispatched
//  2. issues.unassigned where assignee.login == "Copilot"           -> canceled
//  3. pull_request.opened|reopened|synchronize where
//     pull_request.user.login == "copilot-swe-agent[bot]"           -> completed
//     (synchronize is kept so we log every push from the bot; the
//     aggregator de-dupes by (repo, pr) when counting completions.)
func DetectCopilotAgentEvent(githubEvent, action string, payload map[string]interface{}) *CopilotAgentEvent {
	repo := getNestedString(payload, "repository", "full_name")
	org := getNestedString(payload, "repository", "owner", "login")
	if repo == "" {
		return nil
	}

	switch githubEvent {
	case "issues":
		if action != "assigned" && action != "unassigned" {
			return nil
		}
		// GitHub sends the specific assignee that changed on `assignee`,
		// not the full list.
		assignee := getNestedString(payload, "assignee", "login")
		if !isCopilotLogin(assignee) {
			return nil
		}
		kind := CopilotAgentDispatched
		if action == "unassigned" {
			kind = CopilotAgentCanceled
		}
		return &CopilotAgentEvent{
			Kind:  kind,
			Org:   org,
			Repo:  repo,
			Issue: int(getNestedNumber(payload, "issue", "number")),
			Actor: getNestedString(payload, "sender", "login"),
		}

	case "pull_request":
		if action != "opened" && action != "reopened" && action != "synchronize" {
			return nil
		}
		author := getNestedString(payload, "pull_request", "user", "login")
		if !isCopilotSWEBot(author) {
			return nil
		}
		return &CopilotAgentEvent{
			Kind:  CopilotAgentCompleted,
			Org:   org,
			Repo:  repo,
			PR:    int(getNestedNumber(payload, "pull_request", "number")),
			Issue: copilotLinkedIssue(payload),
			Actor: author,
		}
	}
	return nil
}

func isCopilotLogin(login string) bool {
	l := strings.ToLower(login)
	// The assignee login is literally "Copilot" on the issues.assigned
	// event for the Enterprise coding agent.
	return l == "copilot" || l == "copilot-swe-agent[bot]" || l == "copilot-swe-agent"
}

func isCopilotSWEBot(login string) bool {
	return strings.EqualFold(login, CopilotAgentBot) ||
		strings.EqualFold(login, "copilot-swe-agent")
}

// copilotLinkedIssue best-effort extracts a "Fixes #N" / "Closes #N" /
// "Resolves #N" issue number from the PR body so completions can be
// joined back to the original dispatch. Returns 0 if none found.
func copilotLinkedIssue(payload map[string]interface{}) int {
	body := getNestedString(payload, "pull_request", "body")
	if body == "" {
		return 0
	}
	keywords := []string{"fixes", "closes", "resolves", "fix", "close", "resolve"}
	lower := strings.ToLower(body)
	for _, kw := range keywords {
		idx := strings.Index(lower, kw+" #")
		if idx < 0 {
			continue
		}
		rest := lower[idx+len(kw)+2:]
		var n int
		for i := 0; i < len(rest); i++ {
			c := rest[i]
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		if n > 0 {
			return n
		}
	}
	return 0
}

// ToDispatchRecord synthesizes a DispatchRecord so the Copilot session
// is counted in swarm_today and visible to Sentinel. dispatchID is
// supplied by the caller — for `completed` events the caller should
// reuse the id minted on the earlier `dispatched` record (joined by
// repo+issue) to keep the correlation chain intact; if none is known,
// pass a fresh id.
func (e *CopilotAgentEvent) ToDispatchRecord(dispatchID string, now time.Time) DispatchRecord {
	evt := Event{
		Source: "github",
		Repo:   e.Repo,
		Payload: map[string]string{
			"org":   e.Org,
			"actor": e.Actor,
		},
	}
	if e.Issue > 0 {
		evt.Payload["issue"] = fmt.Sprintf("%d", e.Issue)
	}
	if e.PR > 0 {
		evt.Payload["pr"] = fmt.Sprintf("%d", e.PR)
	}

	var result string
	switch e.Kind {
	case CopilotAgentDispatched:
		evt.Type = EventIssueLabeled // nearest fit; the label key carries "copilot-assigned"
		evt.Payload["label"] = "copilot-assigned"
		result = "dispatched"
	case CopilotAgentCompleted:
		evt.Type = EventCompletion
		result = "completed"
	case CopilotAgentCanceled:
		evt.Type = EventIssueLabeled
		evt.Payload["label"] = "copilot-unassigned"
		result = "canceled"
	}

	return DispatchRecord{
		Agent:      "copilot-agent",
		Event:      evt,
		Result:     result,
		Reason:     fmt.Sprintf("copilot-agent %s: %s#%d", e.Kind, e.Repo, maxInt(e.Issue, e.PR)),
		Driver:     CopilotAgentDriver,
		Tier:       "copilot",
		DispatchID: dispatchID,
		Timestamp:  now.UTC().Format(time.RFC3339),
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// RecordCopilotAgentEvent persists the synthesized DispatchRecord to
// Redis on the same dispatch-log key the regular dispatcher uses, so
// classifyTiers() in swarm_today picks it up with no extra plumbing.
//
// Correlation: for `completed` we look up the most recent
// `dispatched` row for (repo, issue) in the last 500 records and reuse
// its DispatchID. Fall back to a fresh id if none is found (e.g.
// webhook delivery out of order, or the dispatch row rolled off the
// trim window).
func (d *Dispatcher) RecordCopilotAgentEvent(ctx context.Context, ev *CopilotAgentEvent) error {
	if ev == nil || d == nil || d.rdb == nil {
		return nil
	}
	dispatchID := ""
	if ev.Kind == CopilotAgentCompleted && ev.Issue > 0 {
		dispatchID = d.findCopilotDispatchID(ctx, ev.Repo, ev.Issue)
	}
	if dispatchID == "" {
		dispatchID = newDispatchID()
	}
	rec := ev.ToDispatchRecord(dispatchID, time.Now())
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	pipe := d.rdb.Pipeline()
	pipe.LPush(ctx, d.key("dispatch-log"), data)
	pipe.LTrim(ctx, d.key("dispatch-log"), 0, 499)
	_, err = pipe.Exec(ctx)
	return err
}

// findCopilotDispatchID scans recent records for a Copilot dispatch
// on the same (repo, issue). Best-effort — empty string on miss.
func (d *Dispatcher) findCopilotDispatchID(ctx context.Context, repo string, issue int) string {
	recs, err := d.RecentDispatches(ctx, 500)
	if err != nil {
		return ""
	}
	target := fmt.Sprintf("%d", issue)
	for _, r := range recs {
		if r.Driver != CopilotAgentDriver || r.Result != "dispatched" {
			continue
		}
		if r.Event.Repo != repo {
			continue
		}
		if r.Event.Payload["issue"] != target {
			continue
		}
		return r.DispatchID
	}
	return ""
}
