package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/learner"
)

const (
	openclawDefaultTimeout    = 5 * time.Minute
	openclawPollInterval      = 5 * time.Second
	openclawDefaultHomeserver = "http://localhost:8008"
)

// OpenClawAdapter dispatches tasks to an OpenClaw agent via Matrix messages.
// Octi sends a task message to a Matrix room where the OpenClaw bot is joined,
// waits for the bot's response, and returns the result.
type OpenClawAdapter struct {
	homeserver  string // Matrix homeserver URL
	accessToken string // Octi's Matrix access token
	roomID      string // Dispatch room ID
	botUserID   string // OpenClaw bot's Matrix user ID
	timeout     time.Duration
	learner     *learner.Learner
}

// NewOpenClawAdapter creates an OpenClawAdapter from environment variables or explicit params.
// Env vars: MATRIX_HOMESERVER, OCTI_MATRIX_TOKEN, OPENCLAW_ROOM_ID, OPENCLAW_BOT_USER_ID.
func NewOpenClawAdapter(homeserver, token, roomID, botUserID string) *OpenClawAdapter {
	if homeserver == "" {
		homeserver = os.Getenv("MATRIX_HOMESERVER")
	}
	if homeserver == "" {
		homeserver = openclawDefaultHomeserver
	}
	if token == "" {
		token = os.Getenv("OCTI_MATRIX_TOKEN")
	}
	if roomID == "" {
		roomID = os.Getenv("OPENCLAW_ROOM_ID")
	}
	if botUserID == "" {
		botUserID = os.Getenv("OPENCLAW_BOT_USER_ID")
	}
	return &OpenClawAdapter{
		homeserver:  homeserver,
		accessToken: token,
		roomID:      roomID,
		botUserID:   botUserID,
		timeout:     openclawDefaultTimeout,
	}
}

func (a *OpenClawAdapter) Name() string { return "openclaw" }

func (a *OpenClawAdapter) SetLearner(l *learner.Learner) { a.learner = l }

// CanAccept returns true for general tasks when Matrix credentials are configured.
func (a *OpenClawAdapter) CanAccept(task *Task) bool {
	if task == nil {
		return false
	}
	if a.accessToken == "" || a.roomID == "" || a.botUserID == "" {
		return false
	}
	// OpenClaw can handle general tasks — it has tools, web access, etc.
	switch task.Type {
	case "research", "triage", "qa", "prompt_config", "tool_addition":
		return true
	default:
		return false
	}
}

// Dispatch sends a task to the OpenClaw bot via Matrix and waits for a response.
func (a *OpenClawAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	result := &AdapterResult{
		TaskID:  task.ID,
		Adapter: a.Name(),
	}

	// Build the dispatch message
	msg := fmt.Sprintf("[Octi Dispatch] Contract: %s\nType: %s | Priority: %s | Repo: %s\n\n%s",
		task.ID, task.Type, task.Priority, task.Repo, task.Prompt)

	if task.Context != "" {
		msg += "\n\nContext:\n" + task.Context
	}

	// Send the message
	txnID := fmt.Sprintf("octi-%s-%d", task.ID, time.Now().UnixMilli())
	if err := a.sendMessage(ctx, txnID, msg); err != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("send message: %v", err)
		return result, err
	}

	// Poll for the bot's response
	response, err := a.waitForResponse(ctx, txnID)
	if err != nil {
		result.Status = "failed"
		result.Error = fmt.Sprintf("wait for response: %v", err)
		return result, err
	}

	result.Status = "completed"
	result.Output = response

	// Store learning if learner is available
	if a.learner != nil {
		taskInfo := &learner.TaskInfo{
			Type:     task.Type,
			Repo:     task.Repo,
			Prompt:   task.Prompt,
			Priority: task.Priority,
		}
		outcomeInfo := &learner.OutcomeInfo{
			Status:  result.Status,
			Adapter: a.Name(),
			Output:  response,
		}
		_ = a.learner.RecordOutcome(ctx, taskInfo, outcomeInfo)
	}

	return result, nil
}

// sendMessage sends a text message to the dispatch room.
func (a *OpenClawAdapter) sendMessage(ctx context.Context, txnID, body string) error {
	reqURL := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s",
		a.homeserver, url.PathEscape(a.roomID), url.PathEscape(txnID))

	payload, err := json.Marshal(map[string]string{
		"msgtype": "m.text",
		"body":    body,
	})
	if err != nil {
		return fmt.Errorf("marshal message payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", reqURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("matrix send failed (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// waitForResponse polls the room for a response from the bot.
func (a *OpenClawAdapter) waitForResponse(ctx context.Context, afterTxnID string) (string, error) {
	deadline := time.After(a.timeout)
	ticker := time.NewTicker(openclawPollInterval)
	defer ticker.Stop()

	// Get the "from" token for pagination (start from now)
	fromToken, err := a.getSyncToken(ctx)
	if err != nil {
		// Fall back to polling from recent messages
		fromToken = ""
	}

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("timeout waiting for OpenClaw response after %s", a.timeout)
		case <-ticker.C:
			response, found, nextToken, err := a.checkForBotMessage(ctx, fromToken)
			if err != nil {
				// Non-transient HTTP errors should stop polling immediately.
				if isNonTransientError(err) {
					return "", fmt.Errorf("non-transient error polling for response: %w", err)
				}
				continue // transient error, keep polling
			}
			if nextToken != "" {
				fromToken = nextToken
			}
			if found {
				return response, nil
			}
		}
	}
}

// isNonTransientError returns true for HTTP status codes that indicate the
// request will never succeed (auth failures, not found, etc.).
func isNonTransientError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "(401)") ||
		strings.Contains(msg, "(403)") ||
		strings.Contains(msg, "(404)")
}

// getSyncToken gets a pagination token from the room's latest state.
func (a *OpenClawAdapter) getSyncToken(ctx context.Context) (string, error) {
	reqURL := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/messages?dir=b&limit=1",
		a.homeserver, url.PathEscape(a.roomID))

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("matrix messages failed (%d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Start string `json:"start"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Start, nil
}

// checkForBotMessage checks for new messages from the bot in the room.
// Returns (message, found, nextToken, error).
func (a *OpenClawAdapter) checkForBotMessage(ctx context.Context, fromToken string) (string, bool, string, error) {
	reqURL := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/messages?dir=f&limit=10",
		a.homeserver, url.PathEscape(a.roomID))
	if fromToken != "" {
		reqURL += "&from=" + url.QueryEscape(fromToken)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", false, "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", false, "", fmt.Errorf("matrix messages failed (%d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Chunk []struct {
			Type    string `json:"type"`
			Sender  string `json:"sender"`
			Content struct {
				Body    string `json:"body"`
				MsgType string `json:"msgtype"`
			} `json:"content"`
		} `json:"chunk"`
		End string `json:"end"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", false, "", err
	}

	nextToken := result.End

	// Look for a message from the bot (not from us)
	for _, event := range result.Chunk {
		if event.Type != "m.room.message" {
			continue
		}
		if event.Sender != a.botUserID {
			continue
		}
		body := event.Content.Body
		// Skip pairing/error messages
		if strings.Contains(body, "Pairing code") || strings.Contains(body, "access not configured") {
			continue
		}
		if len(body) > 0 {
			return body, true, nextToken, nil
		}
	}

	return "", false, nextToken, nil
}
