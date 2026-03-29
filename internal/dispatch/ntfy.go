package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Ntfy priority levels, matching the ntfy.sh API convention.
// See: https://docs.ntfy.sh/publish/#message-priority
const (
	NtfyPriorityMax     = 5 // Urgent — pops through Do Not Disturb
	NtfyPriorityHigh    = 4 // High
	NtfyPriorityDefault = 3 // Default
	NtfyPriorityLow     = 2 // Low
	NtfyPriorityMin     = 1 // Minimum — no notification sound
)

// NtfyNotifier sends push notifications via ntfy.sh (or a self-hosted ntfy server).
// If baseURL or topic is empty, all Post* methods are no-ops.
//
// Usage:
//
//	n := NewNtfyNotifier("https://ntfy.sh", "agentguard-cto")
//	n.Post(ctx, "Budget exhausted", "codex circuit breaker OPEN", NtfyPriorityHigh)
type NtfyNotifier struct {
	baseURL string
	topic   string
	client  *http.Client
}

// NewNtfyNotifier creates an NtfyNotifier targeting the given base URL and topic.
// baseURL can be "https://ntfy.sh" for the hosted service or a self-hosted URL.
// If either baseURL or topic is empty, the notifier is disabled.
func NewNtfyNotifier(baseURL, topic string) *NtfyNotifier {
	return &NtfyNotifier{
		baseURL: baseURL,
		topic:   topic,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled returns true if both baseURL and topic are configured.
func (n *NtfyNotifier) Enabled() bool {
	return n.baseURL != "" && n.topic != ""
}

// Post sends a push notification with a title and message body.
// priority should be one of the NtfyPriority* constants.
func (n *NtfyNotifier) Post(ctx context.Context, title, message string, priority int) error {
	if !n.Enabled() {
		return nil
	}
	return n.send(ctx, title, message, priority, nil)
}

// PostDriverAlert sends a high-priority notification for a driver circuit open event.
func (n *NtfyNotifier) PostDriverAlert(ctx context.Context, driverName string, failures int) error {
	if !n.Enabled() {
		return nil
	}
	title := fmt.Sprintf("Driver Alert: %s", driverName)
	msg := fmt.Sprintf("Circuit breaker OPEN — %d consecutive failures. Agents rerouting.", failures)
	return n.send(ctx, title, msg, NtfyPriorityHigh, nil)
}

// PostPRReadyAlert sends a default-priority notification when a PR is ready to merge.
func (n *NtfyNotifier) PostPRReadyAlert(ctx context.Context, repo string, prNumber int, title string) error {
	if !n.Enabled() {
		return nil
	}
	notifTitle := fmt.Sprintf("PR Ready: #%d", prNumber)
	msg := fmt.Sprintf("%s — %s/pull/%d\nAll checks green.", title, fmt.Sprintf("https://github.com/%s", repo), prNumber)
	return n.send(ctx, notifTitle, msg, NtfyPriorityDefault, nil)
}

// PostAllDriversDown sends a max-priority notification when all drivers are exhausted.
func (n *NtfyNotifier) PostAllDriversDown(ctx context.Context, description string) error {
	if !n.Enabled() {
		return nil
	}
	return n.send(ctx, "🚨 All Drivers Exhausted", description, NtfyPriorityMax, nil)
}

// send performs the HTTP POST to the ntfy topic endpoint.
// extraHeaders are set as additional HTTP headers (e.g., X-Actions for clickable buttons).
func (n *NtfyNotifier) send(ctx context.Context, title, message string, priority int, extraHeaders map[string]string) error {
	endpoint := fmt.Sprintf("%s/%s", n.baseURL, n.topic)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(message))
	if err != nil {
		return fmt.Errorf("create ntfy request: %w", err)
	}

	req.Header.Set("Content-Type", "text/plain")
	if title != "" {
		req.Header.Set("X-Title", title)
	}
	if priority != NtfyPriorityDefault {
		req.Header.Set("X-Priority", strconv.Itoa(priority))
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %d", resp.StatusCode)
	}
	return nil
}
