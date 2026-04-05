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
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// --- Block Kit message tests ---

func TestNotifier_PostDriverAlert(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostDriverAlert(ctx, "codex", 63); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Must contain blocks, not plain text
	if _, ok := payload["blocks"]; !ok {
		t.Fatal("expected 'blocks' key in payload")
	}
	// blocks must be an array
	blocks, ok := payload["blocks"].([]interface{})
	if !ok || len(blocks) == 0 {
		t.Fatal("expected non-empty blocks array")
	}
}

func TestNotifier_PostDriverAlert_ContainsButtons(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)
	_ = n.PostDriverAlert(ctx, "copilot", 5)

	raw := string(received)
	for _, actionID := range []string{"pause_squad", "switch_tier", "ignore_alert"} {
		if !strings.Contains(raw, actionID) {
			t.Errorf("expected action_id %q in payload", actionID)
		}
	}
	// driver name should appear as value
	if !strings.Contains(raw, "copilot") {
		t.Error("expected driver name in payload")
	}
}

func TestNotifier_PostPRReadyAlert(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostPRReadyAlert(ctx, "chitinhq/octi-pulpo", 42, "feat: my change"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw := string(received)
	if !strings.Contains(raw, "merge_pr") {
		t.Error("expected merge_pr action_id in payload")
	}
	if !strings.Contains(raw, "review_pr") {
		t.Error("expected review_pr action_id in payload")
	}
	if !strings.Contains(raw, "skip_pr") {
		t.Error("expected skip_pr action_id in payload")
	}
	if !strings.Contains(raw, "42") {
		t.Error("expected PR number in payload")
	}
}

func TestNotifier_PostSprintGoalAlert(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostSprintGoalAlert(ctx, "octi-pulpo", "ship Slack control plane"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw := string(received)
	if !strings.Contains(raw, "accept_goal") {
		t.Error("expected accept_goal action_id in payload")
	}
	if !strings.Contains(raw, "request_changes") {
		t.Error("expected request_changes action_id in payload")
	}
	if !strings.Contains(raw, "octi-pulpo") {
		t.Error("expected squad name in payload")
	}
}

func TestNotifier_PostBudgetPausedAlert(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostBudgetPausedAlert(ctx, "octi-pulpo-sr"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw := string(received)
	for _, actionID := range []string{"override_budget", "dismiss_budget_alert"} {
		if !strings.Contains(raw, actionID) {
			t.Errorf("expected action_id %q in budget paused alert", actionID)
		}
	}
	if !strings.Contains(raw, "octi-pulpo-sr") {
		t.Error("expected agent name in budget paused alert")
	}
}

func TestNotifier_BlockKit_NoopWhenDisabled(t *testing.T) {
	ctx := context.Background()
	n := NewNotifier("")

	if err := n.PostDriverAlert(ctx, "codex", 1); err != nil {
		t.Fatalf("PostDriverAlert: %v", err)
	}
	if err := n.PostPRReadyAlert(ctx, "org/repo", 1, "title"); err != nil {
		t.Fatalf("PostPRReadyAlert: %v", err)
	}
	if err := n.PostSprintGoalAlert(ctx, "squad", "goal"); err != nil {
		t.Fatalf("PostSprintGoalAlert: %v", err)
	}
	if err := n.PostBudgetPausedAlert(ctx, "some-agent"); err != nil {
		t.Fatalf("PostBudgetPausedAlert: %v", err)
	}
}

// --- Slack actions endpoint tests ---

// makeSlackPayload builds the URL-encoded body that Slack sends for interactive callbacks.
func makeSlackPayload(actionID, value, user string) string {
	inner, _ := json.Marshal(map[string]interface{}{
		"type": "block_actions",
		"actions": []map[string]interface{}{
			{"action_id": actionID, "value": value},
		},
		"user": map[string]string{"name": user},
	})
	return url.Values{"payload": {string(inner)}}.Encode()
}

// makeSlackSig computes the v0 Slack request signature.
func makeSlackSig(secret []byte, timestamp, body string) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "v0:%s:%s", timestamp, body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookServer_SlackActions_IgnoreAck(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")
	secret := []byte("test-signing-secret")
	ws.SetSlackSigningSecret(secret)

	body := makeSlackPayload("ignore_alert", "codex", "jared")
	ts := "1234567890"
	sig := makeSlackSig(secret, ts, body)

	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	rr := httptest.NewRecorder()

	ws.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid response JSON: %v", err)
	}
	if !strings.Contains(resp["text"], "Acknowledged") {
		t.Errorf("expected acknowledgement text, got: %q", resp["text"])
	}
}

func TestWebhookServer_SlackActions_BadSignature(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")
	ws.SetSlackSigningSecret([]byte("correct-secret"))

	body := makeSlackPayload("ignore_alert", "codex", "jared")
	ts := "1234567890"
	wrongSig := makeSlackSig([]byte("wrong-secret"), ts, body)

	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", wrongSig)
	rr := httptest.NewRecorder()

	ws.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on bad signature, got %d", rr.Code)
	}
}

func TestWebhookServer_SlackActions_NoSignatureCheck_WhenSecretEmpty(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")
	// No signing secret set — should accept any request (useful for local dev)

	body := makeSlackPayload("skip_pr", "chitinhq/octi-pulpo/42", "jared")
	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	ws.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 without signing secret, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestWebhookServer_SlackActions_MethodNotAllowed(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")

	req := httptest.NewRequest(http.MethodGet, "/slack/actions", nil)
	rr := httptest.NewRecorder()

	ws.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestWebhookServer_SlackActions_MissingPayload(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")

	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader("not-a-payload"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	ws.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on missing payload, got %d", rr.Code)
	}
}

func TestWebhookServer_SlackActions_UnknownAction(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")

	body := makeSlackPayload("unknown_action", "somevalue", "jared")
	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	ws.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for unknown action (graceful), got %d", rr.Code)
	}
}

func TestVerifySlackSignature(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")
	secret := []byte("8f742231b10e8888abcd99yyyzzz85a5")
	ws.SetSlackSigningSecret(secret)

	body := []byte("v=test&body=data")
	ts := "1531420618"
	sig := makeSlackSig(secret, ts, string(body))

	if !ws.verifySlackSignature(body, ts, sig) {
		t.Fatal("expected valid signature to pass verification")
	}

	// Tampered body
	if ws.verifySlackSignature([]byte("tampered"), ts, sig) {
		t.Fatal("tampered body should fail verification")
	}

	// Bad sig format
	if ws.verifySlackSignature(body, ts, "sha256=invalidsig") {
		t.Fatal("wrong prefix should fail")
	}
}

// TestNotifier_UpdateSlackMessage_FireAndForget verifies that updateSlackMessage
// does not panic when the response URL is unreachable.
func TestNotifier_UpdateSlackMessage_FireAndForget(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")

	// Should not panic even with an unreachable URL
	ws.updateSlackMessage("http://127.0.0.1:1", "test message")
}

// TestWebhookServer_SlackActions_EmptyActions verifies graceful handling
// when Slack sends a valid payload with no actions (edge case).
func TestWebhookServer_SlackActions_EmptyActions(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")

	inner, _ := json.Marshal(map[string]interface{}{
		"type":    "block_actions",
		"actions": []interface{}{},
		"user":    map[string]string{"name": "jared"},
	})
	body := url.Values{"payload": {string(inner)}}.Encode()

	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	ws.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty actions, got %d", rr.Code)
	}
}

func TestRouteSlackAction_Context(t *testing.T) {
	d, ctx := testSetup(t)
	ws := NewWebhookServer(d, "")

	// ignore_alert, review_pr, skip_pr should all return ack without Redis ops
	for _, actionID := range []string{"ignore_alert", "review_pr", "skip_pr"} {
		ack, err := ws.routeSlackAction(ctx, actionID, "somevalue", "testuser")
		if err != nil {
			t.Errorf("routeSlackAction(%q): %v", actionID, err)
		}
		if !strings.Contains(ack, "testuser") {
			t.Errorf("routeSlackAction(%q) ack missing actor: %q", actionID, ack)
		}
	}
}

// TestRouteSlackAction_DismissBudgetAlert verifies dismiss is a no-op acknowledgement.
func TestRouteSlackAction_DismissBudgetAlert(t *testing.T) {
	d, ctx := testSetup(t)
	ws := NewWebhookServer(d, "")

	ack, err := ws.routeSlackAction(ctx, "dismiss_budget_alert", "octi-pulpo-sr", "jared")
	if err != nil {
		t.Fatalf("routeSlackAction(dismiss_budget_alert): %v", err)
	}
	if !strings.Contains(ack, "octi-pulpo-sr") {
		t.Errorf("expected agent name in ack, got %q", ack)
	}
	if !strings.Contains(ack, "jared") {
		t.Errorf("expected actor name in ack, got %q", ack)
	}
	if !strings.Contains(strings.ToLower(ack), "paused") {
		t.Errorf("expected 'paused' in dismiss ack, got %q", ack)
	}
}

// TestRouteSlackAction_OverrideBudget_NoBudgetStore verifies an error is returned
// when the budget store is not configured.
func TestRouteSlackAction_OverrideBudget_NoBudgetStore(t *testing.T) {
	d, ctx := testSetup(t)
	ws := NewWebhookServer(d, "")
	// No budget store set

	_, err := ws.routeSlackAction(ctx, "override_budget", "octi-pulpo-sr", "jared")
	if err == nil {
		t.Fatal("expected error when budget store is not configured")
	}
}

// TestWebhookServer_SlackActions_DismissBudgetAlert verifies full HTTP round-trip.
func TestWebhookServer_SlackActions_DismissBudgetAlert(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")

	body := makeSlackPayload("dismiss_budget_alert", "octi-pulpo-sr", "jared")
	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	ws.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if !strings.Contains(resp["text"], "octi-pulpo-sr") {
		t.Errorf("expected agent name in response, got %q", resp["text"])
	}
}
