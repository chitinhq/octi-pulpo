package dispatch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SlackInteractionHandler processes Slack interactive component callbacks.
//
// When a user clicks a button embedded in a Slack Block Kit message, Slack
// POSTs to the /slack/actions endpoint as application/x-www-form-urlencoded
// with a "payload" field containing the action JSON.
//
// This handler verifies the X-Slack-Signature, parses the block_actions
// payload, and converts the button's action_id into a coord signal or
// dispatch event so the swarm can react without the CTO lifting a finger.
type SlackInteractionHandler struct {
	signingSecret []byte
	dispatcher    *Dispatcher
	agentID       string
}

// NewSlackInteractionHandler creates a handler for Slack button callbacks.
// signingSecret is read from SLACK_SIGNING_SECRET and used to verify the
// X-Slack-Signature header. agentID is the identity used when broadcasting
// coord signals (e.g. "octi-pulpo-daemon").
func NewSlackInteractionHandler(signingSecret string, dispatcher *Dispatcher, agentID string) *SlackInteractionHandler {
	return &SlackInteractionHandler{
		signingSecret: []byte(signingSecret),
		dispatcher:    dispatcher,
		agentID:       agentID,
	}
}

// slackActionPayload is the minimal subset of the Slack block_actions payload.
type slackActionPayload struct {
	Type string `json:"type"`
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

// verifySlackSignature validates X-Slack-Signature against the request body.
//
// Slack signs requests as: HMAC-SHA256("v0:<timestamp>:<body>") → "v0=<hex>"
// Requests older than 5 minutes are rejected to prevent replay attacks.
// If no signing secret is configured, all requests are accepted (dev mode).
func (h *SlackInteractionHandler) verifySlackSignature(timestamp string, body []byte, sig string) bool {
	if len(h.signingSecret) == 0 {
		return true // no secret — dev/test mode
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	age := time.Since(time.Unix(ts, 0))
	if age > 5*time.Minute || age < -time.Minute {
		return false // stale or future-dated: replay attempt
	}

	if !strings.HasPrefix(sig, "v0=") {
		return false
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(sig, "v0="))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, h.signingSecret)
	fmt.Fprintf(mac, "v0:%s:", timestamp)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), sigBytes)
}

// Handle processes one Slack interactive callback (button click).
// It reads the request body, verifies the signature, and routes the action.
// Returns a human-readable confirmation string for the ephemeral Slack response.
func (h *SlackInteractionHandler) Handle(ctx context.Context, r *http.Request) (string, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	ts := r.Header.Get("X-Slack-Request-Timestamp")
	sig := r.Header.Get("X-Slack-Signature")
	if !h.verifySlackSignature(ts, body, sig) {
		return "", fmt.Errorf("invalid slack signature")
	}

	// Slack encodes the payload as URL form data: payload=<JSON>
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return "", fmt.Errorf("parse form: %w", err)
	}
	raw := form.Get("payload")
	if raw == "" {
		return "", fmt.Errorf("missing payload field")
	}

	var p slackActionPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return "", fmt.Errorf("unmarshal payload: %w", err)
	}

	if p.Type != "block_actions" || len(p.Actions) == 0 {
		return "ok", nil // not a button click — silently ACK
	}

	username := p.User.Username
	if username == "" {
		username = p.User.ID
	}

	action := p.Actions[0]
	return h.dispatchAction(ctx, action.ActionID, action.Value, username)
}

// dispatchAction converts an action_id into a coord signal or dispatch event.
// Returns a human-readable confirmation for the ephemeral Slack response.
//
// Known action_ids:
//   - pause_squad    → broadcasts "directive: pause-squad" coord signal
//   - switch_tier    → broadcasts "directive: switch-tier" coord signal
//   - merge_pr       → dispatches EventSlackAction for the PR merger agent
//   - ignore_alert   → ACK only, no swarm action
//   - <anything>     → broadcasts a generic "directive: slack-action:<id>" signal
func (h *SlackInteractionHandler) dispatchAction(ctx context.Context, actionID, value, username string) (string, error) {
	coord := h.dispatcher.Coord()

	switch actionID {
	case "pause_squad":
		payload := fmt.Sprintf("pause-squad triggered by %s", username)
		if err := coord.Broadcast(ctx, h.agentID, "directive", payload); err != nil {
			return "", fmt.Errorf("broadcast pause-squad: %w", err)
		}
		return fmt.Sprintf("✅ Pause-squad directive broadcast by @%s", username), nil

	case "switch_tier":
		payload := fmt.Sprintf("switch-tier triggered by %s", username)
		if err := coord.Broadcast(ctx, h.agentID, "directive", payload); err != nil {
			return "", fmt.Errorf("broadcast switch-tier: %w", err)
		}
		return fmt.Sprintf("✅ Switch-tier directive broadcast by @%s", username), nil

	case "merge_pr":
		event := Event{
			Type:     EventSlackAction,
			Source:   "slack",
			Priority: 0,
			Payload:  map[string]string{"action": "merge_pr", "value": value, "by": username},
		}
		if _, err := h.dispatcher.DispatchEvent(ctx, event); err != nil {
			return "", fmt.Errorf("dispatch merge_pr: %w", err)
		}
		return fmt.Sprintf("✅ PR merge dispatched by @%s", username), nil

	case "ignore_alert":
		return fmt.Sprintf("🙈 Alert acknowledged by @%s", username), nil

	default:
		payload := fmt.Sprintf("slack-action:%s value:%s by:%s", actionID, value, username)
		if err := coord.Broadcast(ctx, h.agentID, "directive", payload); err != nil {
			return "", fmt.Errorf("broadcast action: %w", err)
		}
		return fmt.Sprintf("✅ Action *%s* dispatched by @%s", actionID, username), nil
	}
}
