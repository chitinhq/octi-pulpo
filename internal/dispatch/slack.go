package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/AgentGuardHQ/octi-pulpo/internal/standup"
)

// Notifier posts structured notifications to a Slack incoming webhook.
// It supports both plain-text messages and interactive Block Kit messages
// with action buttons. If no webhook URL is configured, all Post* methods
// are no-ops.
type Notifier struct {
	webhookURL string
	client     *http.Client
}

// NewNotifier creates a Notifier. If webhookURL is empty, all Post* calls are silent no-ops.
func NewNotifier(webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled returns true if a Slack webhook URL is configured.
func (n *Notifier) Enabled() bool {
	return n.webhookURL != ""
}

// PostBudgetDashboard sends a periodic driver health summary to Slack.
// workerOK and workerFail are cumulative counters from Redis.
func (n *Notifier) PostBudgetDashboard(ctx context.Context, drivers []routing.DriverHealth, workerOK, workerFail int64) error {
	if !n.Enabled() {
		return nil
	}

	now := time.Now().UTC().Format("15:04 UTC")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*📊 Driver Budget Status (%s)*\n", now))

	for _, d := range drivers {
		icon := "🟢"
		if d.CircuitState == "OPEN" {
			icon = "🔴"
		} else if d.CircuitState == "HALF" {
			icon = "🟡"
		}
		line := fmt.Sprintf("  %s *%s*: %s", icon, d.Name, d.CircuitState)
		if d.Failures > 0 {
			line += fmt.Sprintf(", %d failures", d.Failures)
		}
		sb.WriteString(line + "\n")
	}

	total := workerOK + workerFail
	if total > 0 {
		passRate := float64(workerOK) / float64(total) * 100
		sb.WriteString(fmt.Sprintf("\nPass rate: *%.1f%%* | OK: %d | Failed: %d", passRate, workerOK, workerFail))
	}

	return n.post(ctx, map[string]interface{}{"text": sb.String()})
}

// PostDriversDown posts a Slack alert when all circuit breakers are OPEN.
func (n *Notifier) PostDriversDown(ctx context.Context, description string) error {
	if !n.Enabled() {
		return nil
	}
	text := fmt.Sprintf("🚨 *All Drivers Exhausted*\n%s\nDispatch is paused until at least one driver recovers.", description)
	return n.post(ctx, map[string]interface{}{"text": text})
}

// PostDriversRecovered posts a Slack alert when drivers recover after exhaustion.
func (n *Notifier) PostDriversRecovered(ctx context.Context) error {
	if !n.Enabled() {
		return nil
	}
	return n.post(ctx, map[string]interface{}{"text": "✅ *Drivers Recovered* — dispatch resumed"})
}

// PostDriverAlert sends an interactive Block Kit message for a single driver circuit alert.
// It includes [Pause Squad], [Switch Tier], and [Ignore] action buttons.
// Button clicks are routed back to the /slack/actions endpoint.
func (n *Notifier) PostDriverAlert(ctx context.Context, driverName string, failures int) error {
	if !n.Enabled() {
		return nil
	}

	msg := fmt.Sprintf(
		"🔴 *Driver Alert: `%s`*\nCircuit breaker OPEN — %d consecutive failures.\nAgents are being rerouted to available drivers.",
		driverName, failures,
	)

	blocks := []map[string]interface{}{
		blockSection(msg),
		blockActions(
			slackButton("pause_squad", driverName, "Pause Squad", "danger"),
			slackButton("switch_tier", driverName, "Switch Tier", "primary"),
			slackButton("ignore_alert", driverName, "Ignore", ""),
		),
	}

	return n.postBlocks(ctx, blocks)
}

// PostPRReadyAlert sends an interactive Block Kit message for a PR ready to merge.
// It includes [Merge], [Review], and [Skip] action buttons.
func (n *Notifier) PostPRReadyAlert(ctx context.Context, repo string, prNumber int, title string) error {
	if !n.Enabled() {
		return nil
	}

	prURL := fmt.Sprintf("https://github.com/%s/pull/%d", repo, prNumber)
	msg := fmt.Sprintf(
		"🟡 *PR Ready to Merge*\n<%s|#%d: %s>\nAll checks green — awaiting merge decision.",
		prURL, prNumber, title,
	)
	prKey := fmt.Sprintf("%s/%d", repo, prNumber)

	blocks := []map[string]interface{}{
		blockSection(msg),
		blockActions(
			slackButton("merge_pr", prKey, "Merge", "primary"),
			slackButton("review_pr", prKey, "Review", ""),
			slackButton("skip_pr", prKey, "Skip", ""),
		),
	}

	return n.postBlocks(ctx, blocks)
}

// PostDailyStandup sends the aggregated daily standup to Slack as a Block Kit message.
// Each squad gets one section; traffic-light emoji indicates blocker status.
func (n *Notifier) PostDailyStandup(ctx context.Context, date string, reports []standup.Report) error {
	if !n.Enabled() {
		return nil
	}
	if len(reports) == 0 {
		return nil
	}

	header := fmt.Sprintf("*📋 Daily Standup — %s*", date)
	blocks := []map[string]interface{}{blockSection(header)}

	for _, r := range reports {
		status := "🟢"
		if len(r.Blocked) > 0 {
			if len(r.Blocked) >= 2 {
				status = "🔴"
			} else {
				status = "🟡"
			}
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%s *%s*\n", status, strings.ToUpper(r.Squad)))
		if len(r.Done) > 0 {
			sb.WriteString(fmt.Sprintf("  Done: %s\n", strings.Join(r.Done, ", ")))
		} else {
			sb.WriteString("  Done: nothing\n")
		}
		if len(r.Doing) > 0 {
			sb.WriteString(fmt.Sprintf("  Doing: %s\n", strings.Join(r.Doing, ", ")))
		}
		if len(r.Blocked) > 0 {
			sb.WriteString(fmt.Sprintf("  Blocked: %s\n", strings.Join(r.Blocked, ", ")))
		}
		if len(r.Requests) > 0 {
			sb.WriteString(fmt.Sprintf("  Requests: %s\n", strings.Join(r.Requests, ", ")))
		}

		blocks = append(blocks, blockSection(strings.TrimRight(sb.String(), "\n")))
	}

	return n.postBlocks(ctx, blocks)
}

// PostSprintGoalAlert sends an interactive Block Kit message when a sprint goal is delivered.
// It includes [Accept] and [Request Changes] action buttons.
func (n *Notifier) PostSprintGoalAlert(ctx context.Context, squad, goal string) error {
	if !n.Enabled() {
		return nil
	}

	msg := fmt.Sprintf("🟢 *Sprint Goal Delivered*\nSquad: `%s`\nGoal: %s", squad, goal)

	blocks := []map[string]interface{}{
		blockSection(msg),
		blockActions(
			slackButton("accept_goal", squad, "Accept", "primary"),
			slackButton("request_changes", squad, "Request Changes", ""),
		),
	}

	return n.postBlocks(ctx, blocks)
}

// postBlocks sends a Slack Block Kit payload to the webhook URL.
func (n *Notifier) postBlocks(ctx context.Context, blocks []map[string]interface{}) error {
	return n.post(ctx, map[string]interface{}{"blocks": blocks})
}

// post marshals the payload and POSTs it to the Slack incoming webhook URL.
func (n *Notifier) post(ctx context.Context, payload map[string]interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}
	return nil
}

// blockSection returns a Slack Block Kit section block with mrkdwn text.
func blockSection(text string) map[string]interface{} {
	return map[string]interface{}{
		"type": "section",
		"text": map[string]string{"type": "mrkdwn", "text": text},
	}
}

// blockActions returns a Slack Block Kit actions block containing the given buttons.
func blockActions(buttons ...map[string]interface{}) map[string]interface{} {
	elements := make([]map[string]interface{}, len(buttons))
	copy(elements, buttons)
	return map[string]interface{}{
		"type":     "actions",
		"elements": elements,
	}
}

// slackButton creates a Slack Block Kit button element.
// style can be "primary", "danger", or "" (default/secondary).
func slackButton(actionID, value, text, style string) map[string]interface{} {
	btn := map[string]interface{}{
		"type":      "button",
		"text":      map[string]string{"type": "plain_text", "text": text},
		"action_id": actionID,
		"value":     value,
	}
	if style != "" {
		btn["style"] = style
	}
	return btn
}
