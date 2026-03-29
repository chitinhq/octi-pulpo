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
)

// Notifier posts structured notifications to a Slack incoming webhook.
// If no webhook URL is configured, all Post* methods are no-ops.
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

// ActionButton describes a Slack Block Kit interactive button.
// It is used with PostActionableAlert to add clickable actions to a notification.
type ActionButton struct {
	Text     string // visible button label
	ActionID string // identifier sent back to the /slack/actions endpoint on click
	Value    string // opaque data payload returned with the callback
	Style    string // "primary" (green), "danger" (red), or "" (default gray)
}

// PostActionableAlert sends a Slack Block Kit message with interactive buttons.
// Unlike PostDriversDown, which sends plain text, this embeds the message as a
// Block Kit section + actions block — enabling users to act directly from Slack.
//
// Requires a Slack App (not just an incoming webhook) to receive button callbacks.
// Wire up /slack/actions on the WebhookServer to receive action callbacks.
func (n *Notifier) PostActionableAlert(ctx context.Context, emoji, title, body string, buttons []ActionButton) error {
	if !n.Enabled() {
		return nil
	}

	mdText := fmt.Sprintf("%s *%s*\n%s", emoji, title, body)

	blocks := []map[string]interface{}{
		{
			"type": "section",
			"text": map[string]interface{}{"type": "mrkdwn", "text": mdText},
		},
	}

	if len(buttons) > 0 {
		elements := make([]map[string]interface{}, 0, len(buttons))
		for _, btn := range buttons {
			el := map[string]interface{}{
				"type":      "button",
				"text":      map[string]interface{}{"type": "plain_text", "text": btn.Text, "emoji": true},
				"action_id": btn.ActionID,
				"value":     btn.Value,
			}
			if btn.Style != "" {
				el["style"] = btn.Style
			}
			elements = append(elements, el)
		}
		blocks = append(blocks, map[string]interface{}{
			"type":     "actions",
			"elements": elements,
		})
	}

	return n.post(ctx, map[string]interface{}{
		"text":   mdText, // fallback for clients that don't render Block Kit
		"blocks": blocks,
	})
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
