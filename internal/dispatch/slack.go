package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/chitinhq/octi-pulpo/internal/pipeline"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/chitinhq/octi-pulpo/internal/sprint"
)

// Notifier posts messages to Slack via webhook.
type Notifier struct {
	webhookURL string
	client     *http.Client
}

// NewNotifier creates a new Slack notifier.
func NewNotifier(webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		client:     &http.Client{},
	}
}

// Enabled returns true if the notifier is configured.
func (n *Notifier) Enabled() bool {
	return n.webhookURL != ""
}

// post sends a raw JSON payload to the Slack webhook.
func (n *Notifier) post(ctx context.Context, payload map[string]interface{}) error {
	if !n.Enabled() {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}
	return nil
}

// PostSprintDigest sends a sprint status digest to Slack as a plain-text webhook message.
func (n *Notifier) PostSprintDigest(ctx context.Context, drivers []routing.DriverHealth, ok, fail int64, items []sprint.SprintItem) error {
	if !n.Enabled() {
		return nil
	}
	total := ok + fail
	var pct float64
	if total > 0 {
		pct = float64(ok) / float64(total) * 100
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Sprint Digest*\nPass rate: %.1f%% (%d ok / %d fail)\n", pct, ok, fail))

	for _, d := range drivers {
		icon := "CLOSED"
		if d.CircuitState == "OPEN" {
			icon = "OPEN"
		}
		line := fmt.Sprintf("  %s: %s", d.Name, icon)
		if d.Failures > 0 {
			line += fmt.Sprintf(" (%d failures)", d.Failures)
		}
		sb.WriteString(line + "\n")
	}

	if len(items) > 0 {
		counts := map[string]int{}
		for _, it := range items {
			counts[it.Status]++
		}
		sb.WriteString(fmt.Sprintf("Sprint: Done: %d | PR Open: %d | Open: %d\n", counts["done"], counts["pr_open"], counts["open"]))

		// List items with open PRs
		for _, it := range items {
			if it.Status == "pr_open" && it.PRNumber > 0 {
				sb.WriteString(fmt.Sprintf("  PR #%d: %s\n", it.PRNumber, it.Title))
			}
		}

		// Detect blockers: items that depend on non-done items
		doneSet := map[int]bool{}
		for _, it := range items {
			if it.Status == "done" || it.Status == "pr_open" {
				doneSet[it.IssueNum] = true
			}
		}
		var blockers []string
		for _, it := range items {
			for _, dep := range it.DependsOn {
				if !doneSet[dep] {
					blockers = append(blockers, fmt.Sprintf("  %s blocked by #%d", it.Title, dep))
				}
			}
		}
		if len(blockers) > 0 {
			sb.WriteString("Blockers:\n")
			for _, b := range blockers {
				sb.WriteString(b + "\n")
			}
		}
	}

	return n.post(ctx, map[string]interface{}{"text": sb.String()})
}

// PostBudgetDashboard sends a budget status dashboard to Slack as a plain-text webhook message.
func (n *Notifier) PostBudgetDashboard(ctx context.Context, drivers []routing.DriverHealth, ok, fail int64) error {
	if !n.Enabled() {
		return nil
	}
	total := ok + fail
	var pct float64
	if total > 0 {
		pct = float64(ok) / float64(total) * 100
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Budget Dashboard*\nPass rate: %.1f%% (%d ok / %d fail)\n", pct, ok, fail))
	for _, d := range drivers {
		icon := "CLOSED"
		if d.CircuitState == "OPEN" {
			icon = "OPEN"
		}
		line := fmt.Sprintf("  %s: %s", d.Name, icon)
		if d.Failures > 0 {
			line += fmt.Sprintf(" (%d failures)", d.Failures)
		}
		sb.WriteString(line + "\n")
	}
	return n.post(ctx, map[string]interface{}{"text": sb.String()})
}

// PostDailyStandup sends a daily standup summary to Slack.
func (n *Notifier) PostDailyStandup(ctx context.Context, entries interface{}) error {
	if !n.Enabled() {
		return nil
	}
	return n.post(ctx, map[string]interface{}{"text": "*Daily Standup*\nStandup entries posted."})
}

// PostStuckAgentAlert sends a Slack alert for a stuck agent.
func (n *Notifier) PostStuckAgentAlert(ctx context.Context, agent string, consecutiveFails int) error {
	if !n.Enabled() {
		return nil
	}
	text := fmt.Sprintf("*Stuck Agent:* %s — %d consecutive failures, triage flag set.", agent, consecutiveFails)
	return n.post(ctx, map[string]interface{}{"text": text})
}

// PostInactiveSquadAlert sends a Slack alert for an inactive squad.
func (n *Notifier) PostInactiveSquadAlert(ctx context.Context, squad string, idleHours int) error {
	if !n.Enabled() {
		return nil
	}
	text := fmt.Sprintf("*Inactive Squad:* %s — no activity for %d hours.", squad, idleHours)
	return n.post(ctx, map[string]interface{}{"text": text})
}

// PostDriversDown sends a Slack alert when all drivers are exhausted.
func (n *Notifier) PostDriversDown(ctx context.Context, description string) error {
	if !n.Enabled() {
		return nil
	}
	text := fmt.Sprintf("*All Drivers Exhausted*\n%s", description)
	return n.post(ctx, map[string]interface{}{"text": text})
}

// PostDriversRecovered sends a Slack notification when drivers have recovered.
func (n *Notifier) PostDriversRecovered(ctx context.Context) error {
	if !n.Enabled() {
		return nil
	}
	return n.post(ctx, map[string]interface{}{"text": "*Drivers Recovered* — at least one circuit closed."})
}

// PostAdapterDispatch sends a Slack notification for adapter dispatch results.
func (n *Notifier) PostAdapterDispatch(ctx context.Context, adapter, repo string, issueNum int, status, errMsg string) error {
	if !n.Enabled() {
		return nil
	}
	text := fmt.Sprintf("*Adapter Dispatch:* %s %s#%d → %s", adapter, repo, issueNum, status)
	if errMsg != "" {
		text += "\nError: " + errMsg
	}
	return n.post(ctx, map[string]interface{}{"text": text})
}

// PostDriverAlert sends a Block Kit message for a driver circuit open event.
func (n *Notifier) PostDriverAlert(ctx context.Context, driverName string, failures int) error {
	if !n.Enabled() {
		return nil
	}
	blocks := []interface{}{
		slackSection(fmt.Sprintf("*Driver Alert: %s*\nCircuit breaker OPEN — %d consecutive failures.", driverName, failures)),
		map[string]interface{}{
			"type": "actions",
			"elements": []interface{}{
				slackButton("pause_squad", driverName, "Pause Squad", ""),
				slackButton("switch_tier", driverName, "Switch Tier", ""),
				slackButton("ignore_alert", driverName, "Ignore", ""),
			},
		},
	}
	return n.post(ctx, map[string]interface{}{"blocks": blocks})
}

// PostPRReadyAlert sends a Block Kit message when a PR is ready to merge.
func (n *Notifier) PostPRReadyAlert(ctx context.Context, repo string, prNumber int, title string) error {
	if !n.Enabled() {
		return nil
	}
	value := fmt.Sprintf("%s/%d", repo, prNumber)
	blocks := []interface{}{
		slackSection(fmt.Sprintf("*PR Ready: #%d*\n%s\n%s/pull/%d — All checks green.", prNumber, title, fmt.Sprintf("https://github.com/%s", repo), prNumber)),
		map[string]interface{}{
			"type": "actions",
			"elements": []interface{}{
				slackButton("merge_pr", value, "Merge", "primary"),
				slackButton("review_pr", value, "Review", ""),
				slackButton("skip_pr", value, "Skip", ""),
			},
		},
	}
	return n.post(ctx, map[string]interface{}{"blocks": blocks})
}

// PostSprintGoalAlert sends a Block Kit message when a sprint goal is set.
func (n *Notifier) PostSprintGoalAlert(ctx context.Context, squad, goal string) error {
	if !n.Enabled() {
		return nil
	}
	blocks := []interface{}{
		slackSection(fmt.Sprintf("*Sprint Goal: %s*\n_%s_", squad, goal)),
		map[string]interface{}{
			"type": "actions",
			"elements": []interface{}{
				slackButton("accept_goal", squad, "Accept", "primary"),
				slackButton("request_changes", squad, "Request Changes", ""),
			},
		},
	}
	return n.post(ctx, map[string]interface{}{"blocks": blocks})
}

// PostBudgetPausedAlert sends a Block Kit message when an agent is paused due to budget exhaustion.
func (n *Notifier) PostBudgetPausedAlert(ctx context.Context, agent string) error {
	if !n.Enabled() {
		return nil
	}
	blocks := []interface{}{
		slackSection(fmt.Sprintf("*Budget Exhausted: %s*\nAgent paused — budget override required to continue.", agent)),
		map[string]interface{}{
			"type": "actions",
			"elements": []interface{}{
				slackButton("override_budget", agent, "Override Budget", "primary"),
				slackButton("dismiss_budget_alert", agent, "Dismiss", ""),
			},
		},
	}
	return n.post(ctx, map[string]interface{}{"blocks": blocks})
}

// PostPipelineDashboard sends a pipeline status dashboard to Slack using Block Kit.
func (n *Notifier) PostPipelineDashboard(
	ctx context.Context,
	depths map[pipeline.Stage]int,
	sessions map[pipeline.Stage]int,
	budgets []routing.DriverHealth,
	bp pipeline.BackpressureAction,
) error {
	if !n.Enabled() {
		return nil
	}

	blocks := FormatPipelineDashboard(depths, sessions, budgets, bp)

	payload := map[string]interface{}{
		"blocks": blocks,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal pipeline dashboard: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}
	return nil
}