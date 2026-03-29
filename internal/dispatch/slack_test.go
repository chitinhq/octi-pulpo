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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

func TestNotifier_Enabled(t *testing.T) {
	if NewNotifier("").Enabled() {
		t.Fatal("empty URL should not be enabled")
	}
	if !NewNotifier("http://example.com/hook").Enabled() {
		t.Fatal("non-empty URL should be enabled")
	}
}

func TestNotifier_NoopWhenDisabled(t *testing.T) {
	ctx := context.Background()
	n := NewNotifier("")

	// All Post* calls must return nil without making any HTTP requests.
	if err := n.PostBudgetDashboard(ctx, nil, 0, 0); err != nil {
		t.Fatalf("PostBudgetDashboard: %v", err)
	}
	if err := n.PostDriversDown(ctx, "desc"); err != nil {
		t.Fatalf("PostDriversDown: %v", err)
	}
	if err := n.PostDriversRecovered(ctx); err != nil {
		t.Fatalf("PostDriversRecovered: %v", err)
	}
}

func TestNotifier_PostBudgetDashboard(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	drivers := []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "CLOSED", Failures: 0},
		{Name: "copilot", CircuitState: "OPEN", Failures: 12},
	}

	if err := n.PostBudgetDashboard(ctx, drivers, 80, 20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	text, _ := payload["text"].(string)
	if !strings.Contains(text, "claude-code") {
		t.Error("expected claude-code in dashboard text")
	}
	if !strings.Contains(text, "copilot") {
		t.Error("expected copilot in dashboard text")
	}
	if !strings.Contains(text, "80.0%") {
		t.Errorf("expected 80.0%% pass rate, got: %s", text)
	}
}

func TestNotifier_PostDriversDown(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostDriversDown(ctx, "all circuit breakers OPEN"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	text, _ := payload["text"].(string)
	if !strings.Contains(text, "All Drivers Exhausted") {
		t.Errorf("expected 'All Drivers Exhausted' in text, got: %s", text)
	}
	if !strings.Contains(text, "all circuit breakers OPEN") {
		t.Errorf("expected description in text, got: %s", text)
	}
}

func TestNotifier_PostDriversRecovered(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostDriversRecovered(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	text, _ := payload["text"].(string)
	if !strings.Contains(text, "Drivers Recovered") {
		t.Errorf("expected 'Drivers Recovered' in text, got: %s", text)
	}
}

func TestNotifier_WebhookError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	err := n.PostDriversRecovered(ctx)
	if err == nil {
		t.Fatal("expected error on non-200 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error, got: %v", err)
	}
}

func TestBrain_SetNotifier(t *testing.T) {
	d, _ := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	n := NewNotifier("") // disabled
	brain.SetNotifier(n)

	if brain.notifier != n {
		t.Fatal("SetNotifier did not set the notifier")
	}
}

func TestBrain_MaybePostDashboard_NoopWhenDisabled(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())
	brain.SetNotifier(NewNotifier("")) // disabled

	// Should not panic or error even with no-op notifier
	brain.maybePostDashboard(ctx)
}

func TestBrain_MaybeNotifyConstraintChange_EdgeTriggered(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())
	brain.SetNotifier(NewNotifier(srv.URL))

	downConstraint := Constraint{Type: "all_drivers_down", Description: "all down", Severity: 0}
	noneConstraint := Constraint{Type: "none", Description: "healthy", Severity: 2}

	// First down transition: should fire PostActionableAlert (replaces PostDriversDown)
	brain.maybeNotifyConstraintChange(ctx, downConstraint)
	if callCount != 1 {
		t.Fatalf("expected 1 Slack call on first down transition, got %d", callCount)
	}

	// Still down: should NOT fire again (edge-triggered)
	brain.maybeNotifyConstraintChange(ctx, downConstraint)
	if callCount != 1 {
		t.Fatalf("expected no additional Slack call when still down, got %d", callCount)
	}

	// Recovery transition: should fire PostDriversRecovered
	brain.maybeNotifyConstraintChange(ctx, noneConstraint)
	if callCount != 2 {
		t.Fatalf("expected 1 additional Slack call on recovery, got %d", callCount)
	}

	// Still healthy: no additional calls
	brain.maybeNotifyConstraintChange(ctx, noneConstraint)
	if callCount != 2 {
		t.Fatalf("expected no additional Slack call when still healthy, got %d", callCount)
	}
}

// ── PostActionableAlert tests ─────────────────────────────────────────────────

func TestNotifier_PostActionableAlert_NoopWhenDisabled(t *testing.T) {
	n := NewNotifier("")
	err := n.PostActionableAlert(context.Background(), "🚨", "Title", "body", []ActionButton{
		{Text: "OK", ActionID: "ok", Value: "ok"},
	})
	if err != nil {
		t.Fatalf("expected no-op, got: %v", err)
	}
}

func TestNotifier_PostActionableAlert_BlockKitStructure(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(srv.URL)
	buttons := []ActionButton{
		{Text: "Pause Squad", ActionID: "pause_squad", Value: "pause_squad", Style: "danger"},
		{Text: "Switch Tier", ActionID: "switch_tier", Value: "switch_tier"},
		{Text: "Ignore", ActionID: "ignore_alert", Value: "ignore"},
	}

	if err := n.PostActionableAlert(context.Background(), "🚨", "All Drivers Exhausted", "all OPEN", buttons); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Must have both "text" fallback and "blocks"
	if _, ok := payload["text"]; !ok {
		t.Error("expected 'text' fallback key")
	}
	blocks, ok := payload["blocks"].([]interface{})
	if !ok || len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks (section + actions), got: %v", payload["blocks"])
	}

	// First block must be a section with mrkdwn
	section := blocks[0].(map[string]interface{})
	if section["type"] != "section" {
		t.Errorf("expected section block, got: %v", section["type"])
	}

	// Second block must be actions with our 3 buttons
	actions := blocks[1].(map[string]interface{})
	if actions["type"] != "actions" {
		t.Errorf("expected actions block, got: %v", actions["type"])
	}
	elements, ok := actions["elements"].([]interface{})
	if !ok || len(elements) != 3 {
		t.Fatalf("expected 3 button elements, got %d", len(elements))
	}

	// Verify danger style on first button
	first := elements[0].(map[string]interface{})
	if first["style"] != "danger" {
		t.Errorf("expected danger style on first button, got: %v", first["style"])
	}
	if first["action_id"] != "pause_squad" {
		t.Errorf("expected action_id=pause_squad, got: %v", first["action_id"])
	}
}

func TestNotifier_PostActionableAlert_NoButtons(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(srv.URL)
	if err := n.PostActionableAlert(context.Background(), "🟢", "All Clear", "systems nominal", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	blocks := payload["blocks"].([]interface{})
	if len(blocks) != 1 {
		t.Errorf("expected 1 block (section only, no actions), got %d", len(blocks))
	}
}

// ── SlackInteractionHandler signature verification ────────────────────────────

// makeSlackSig builds a valid X-Slack-Signature for testing.
func makeSlackSig(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%s:", timestamp)
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestSlackInteractionHandler_VerifySignature_Valid(t *testing.T) {
	secret := "test-secret"
	h := NewSlackInteractionHandler(secret, nil, "")
	body := []byte("payload=hello")
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := makeSlackSig(secret, ts, body)

	if !h.verifySlackSignature(ts, body, sig) {
		t.Fatal("expected valid signature to pass")
	}
}

func TestSlackInteractionHandler_VerifySignature_WrongSecret(t *testing.T) {
	h := NewSlackInteractionHandler("correct-secret", nil, "")
	body := []byte("payload=hello")
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := makeSlackSig("wrong-secret", ts, body)

	if h.verifySlackSignature(ts, body, sig) {
		t.Fatal("expected wrong secret to fail")
	}
}

func TestSlackInteractionHandler_VerifySignature_StaleTimestamp(t *testing.T) {
	secret := "test-secret"
	h := NewSlackInteractionHandler(secret, nil, "")
	body := []byte("payload=hello")
	staleTS := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	sig := makeSlackSig(secret, staleTS, body)

	if h.verifySlackSignature(staleTS, body, sig) {
		t.Fatal("expected stale timestamp to fail")
	}
}

func TestSlackInteractionHandler_VerifySignature_NoSecret_DevMode(t *testing.T) {
	h := NewSlackInteractionHandler("", nil, "") // no secret = dev mode
	// Any signature should pass
	if !h.verifySlackSignature("", []byte("body"), "garbage") {
		t.Fatal("expected dev mode (no secret) to accept any signature")
	}
}

// ── SlackInteractionHandler.Handle tests ─────────────────────────────────────

func makeSlackRequest(t *testing.T, secret string, actionID, value, username string) *http.Request {
	t.Helper()

	p := map[string]interface{}{
		"type": "block_actions",
		"user": map[string]string{"id": "U001", "username": username},
		"actions": []map[string]string{
			{"action_id": actionID, "value": value},
		},
	}
	payloadJSON, _ := json.Marshal(p)
	body := url.Values{"payload": {string(payloadJSON)}}.Encode()

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := makeSlackSig(secret, ts, []byte(body))

	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

func TestSlackInteractionHandler_IgnoreAlert(t *testing.T) {
	d, ctx := testSetup(t)
	h := NewSlackInteractionHandler("", d, "test-agent") // no secret — dev mode

	req := makeSlackRequest(t, "", "ignore_alert", "ignore", "jared")
	msg, err := h.Handle(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "acknowledged") {
		t.Errorf("expected acknowledgement message, got: %s", msg)
	}
}

func TestSlackInteractionHandler_PauseSquad(t *testing.T) {
	d, ctx := testSetup(t)
	h := NewSlackInteractionHandler("", d, "test-agent")

	req := makeSlackRequest(t, "", "pause_squad", "pause_squad", "jared")
	msg, err := h.Handle(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "Pause-squad") {
		t.Errorf("expected pause-squad confirmation, got: %s", msg)
	}
}

func TestSlackInteractionHandler_InvalidSignature(t *testing.T) {
	d, ctx := testSetup(t)
	h := NewSlackInteractionHandler("real-secret", d, "test-agent")

	req := makeSlackRequest(t, "wrong-secret", "ignore_alert", "ignore", "jared")
	_, err := h.Handle(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "invalid slack signature") {
		t.Fatalf("expected signature error, got: %v", err)
	}
}

func TestSlackInteractionHandler_NonActionEvent(t *testing.T) {
	d, ctx := testSetup(t)
	h := NewSlackInteractionHandler("", d, "test-agent")

	// A non-block_actions type should ACK silently
	p := map[string]interface{}{"type": "shortcut"}
	payloadJSON, _ := json.Marshal(p)
	body := url.Values{"payload": {string(payloadJSON)}}.Encode()

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", makeSlackSig("", ts, []byte(body)))

	msg, err := h.Handle(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != "ok" {
		t.Errorf("expected 'ok' for non-action event, got: %s", msg)
	}
}

// ── WebhookServer /slack/actions endpoint ────────────────────────────────────

func TestWebhookServer_SlackActions_MethodNotAllowed(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "")
	ws.SetSlackInteractions(NewSlackInteractionHandler("", d, "test"))

	req := httptest.NewRequest(http.MethodGet, "/slack/actions", nil)
	w := httptest.NewRecorder()
	ws.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestWebhookServer_SlackActions_NotConfigured(t *testing.T) {
	d, _ := testSetup(t)
	ws := NewWebhookServer(d, "") // no SetSlackInteractions called — but route isn't registered

	// POST to /slack/actions without SetSlackInteractions returns 404 (route not registered)
	req := httptest.NewRequest(http.MethodPost, "/slack/actions", nil)
	w := httptest.NewRecorder()
	ws.ServeHTTP(w, req)

	// Route not registered → 404
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when route not registered, got %d", w.Code)
	}
}

func TestWebhookServer_SlackActions_IgnoreAlert(t *testing.T) {
	d, ctx := testSetup(t)
	_ = ctx
	ws := NewWebhookServer(d, "")
	ws.SetSlackInteractions(NewSlackInteractionHandler("", d, "test"))

	req := makeSlackRequest(t, "", "ignore_alert", "ignore", "jared")
	req.URL.Path = "/slack/actions"
	w := httptest.NewRecorder()
	ws.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["response_type"] != "ephemeral" {
		t.Errorf("expected ephemeral response, got: %v", resp["response_type"])
	}
	text, _ := resp["text"].(string)
	if !strings.Contains(text, "acknowledged") {
		t.Errorf("expected acknowledgement in response text, got: %s", text)
	}
}
