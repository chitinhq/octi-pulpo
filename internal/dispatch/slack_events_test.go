package dispatch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestSlackHandler creates a SlackEventHandler with no signing secret (dev mode)
// and no bot token (no outbound posting).
func newTestSlackHandler() *SlackEventHandler {
	return NewSlackEventHandler("", "", nil)
}

// TestSlackEventHandler_URLVerificationChallenge verifies the Slack url_verification
// handshake returns the challenge value.
func TestSlackEventHandler_URLVerificationChallenge(t *testing.T) {
	h := newTestSlackHandler()

	payload := `{"type":"url_verification","challenge":"abc123"}`
	req := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(payload))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["challenge"] != "abc123" {
		t.Errorf("expected challenge=abc123, got %q", resp["challenge"])
	}
}

// TestSlackEventHandler_MethodNotAllowed verifies GET is rejected.
func TestSlackEventHandler_MethodNotAllowed(t *testing.T) {
	h := newTestSlackHandler()
	req := httptest.NewRequest(http.MethodGet, "/slack/events", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestSlackEventHandler_IgnoreBotMessage verifies bot-posted messages are silently dropped.
func TestSlackEventHandler_IgnoreBotMessage(t *testing.T) {
	h := newTestSlackHandler()

	// Messages with bot_id set should be ignored.
	payload := `{
		"type":"event_callback",
		"event":{
			"type":"message",
			"text":"status",
			"user":"U123",
			"channel":"C456",
			"ts":"111.222",
			"bot_id":"B789"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(payload))
	w := httptest.NewRecorder()
	h.Handle(w, req)
	// ACKs with 200 but does not trigger any command.
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TestSlackEventHandler_UnknownEventType verifies unknown event_callback events are ACKed.
func TestSlackEventHandler_UnknownEventType(t *testing.T) {
	h := newTestSlackHandler()

	payload := `{"type":"app_rate_limited"}`
	req := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(payload))
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TestParseCommand covers the command keyword extraction rules.
func TestParseCommand(t *testing.T) {
	h := newTestSlackHandler()

	cases := []struct {
		input   string
		wantCmd string
		wantArgs string
	}{
		{"status", "status", ""},
		{"what's the status", "status", ""},
		{"whats the status", "status", ""},
		{"constraint", "constraint", ""},
		{"what's the constraint", "constraint", ""},
		{"help", "help", ""},
		{"?", "help", ""},
		{"commands", "help", ""},
		{"dispatch kernel-sr", "dispatch", "kernel-sr"},
		{"dispatch kernel-sr at #1376", "dispatch", "kernel-sr at #1376"},
		{"trigger octi-pulpo-sr", "dispatch", "octi-pulpo-sr"},
		{"pause cloud", "pause", "cloud"},
		{"pause cloud squad", "pause", "cloud squad"},
		{"resume kernel", "resume", "kernel"},
		{"resume kernel squad", "resume", "kernel squad"},
		{"unknown gibberish here", "", ""},
		{"", "", ""},
	}

	for _, tc := range cases {
		cmd, args := h.parseCommand(tc.input)
		if cmd != tc.wantCmd || args != tc.wantArgs {
			t.Errorf("parseCommand(%q) = (%q, %q), want (%q, %q)",
				tc.input, cmd, args, tc.wantCmd, tc.wantArgs)
		}
	}
}

// TestParseCommand_StripsMentions verifies @USER mentions are removed before parsing.
func TestParseCommand_StripsMentions(t *testing.T) {
	h := newTestSlackHandler()

	cmd, args := h.parseCommand("<@U12345> status")
	if cmd != "status" || args != "" {
		t.Errorf("expected (status, ''), got (%q, %q)", cmd, args)
	}

	cmd, args = h.parseCommand("<@U12345|octi> dispatch kernel-sr")
	if cmd != "dispatch" || args != "kernel-sr" {
		t.Errorf("expected (dispatch, kernel-sr), got (%q, %q)", cmd, args)
	}
}

// TestSlackEventHandler_VerifySignature_Valid verifies a correctly signed request.
func TestSlackEventHandler_VerifySignature_Valid(t *testing.T) {
	secret := "test-signing-secret"
	h := NewSlackEventHandler(secret, "", nil)

	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/slack/events", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)

	if !h.verifySignature(req, body) {
		t.Fatal("expected valid signature to pass")
	}
}

// TestSlackEventHandler_VerifySignature_WrongSecret rejects tampered signatures.
func TestSlackEventHandler_VerifySignature_WrongSecret(t *testing.T) {
	h := NewSlackEventHandler("correct-secret", "", nil)

	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("wrong-secret"))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/slack/events", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)

	if h.verifySignature(req, body) {
		t.Fatal("expected wrong-secret signature to fail")
	}
}

// TestSlackEventHandler_VerifySignature_Stale rejects requests older than 5 minutes.
func TestSlackEventHandler_VerifySignature_Stale(t *testing.T) {
	secret := "test-secret"
	h := NewSlackEventHandler(secret, "", nil)

	body := []byte(`{}`)
	staleTS := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + staleTS + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/slack/events", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", staleTS)
	req.Header.Set("X-Slack-Signature", sig)

	if h.verifySignature(req, body) {
		t.Fatal("expected stale request to fail")
	}
}

// TestSlackEventHandler_VerifySignature_DevMode accepts all requests when no secret is set.
func TestSlackEventHandler_VerifySignature_DevMode(t *testing.T) {
	h := NewSlackEventHandler("", "", nil) // no secret
	req := httptest.NewRequest(http.MethodPost, "/slack/events", nil)
	// No signature headers — should pass in dev mode.
	if !h.verifySignature(req, []byte("{}")) {
		t.Fatal("dev mode should accept all requests")
	}
}

// TestSlackEventHandler_InvalidSignatureRejects verifies 403 on bad signature.
func TestSlackEventHandler_InvalidSignatureRejects(t *testing.T) {
	h := NewSlackEventHandler("real-secret", "", nil)

	body := []byte(`{"type":"url_verification","challenge":"y"}`)
	req := httptest.NewRequest(http.MethodPost, "/slack/events", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("X-Slack-Signature", "v0=badsig")

	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// TestBuildHelpBlocks verifies the help response contains expected command names.
func TestBuildHelpBlocks(t *testing.T) {
	h := newTestSlackHandler()
	blocks := h.buildHelpBlocks()
	if len(blocks) == 0 {
		t.Fatal("expected at least one block")
	}
	data, _ := json.Marshal(blocks)
	text := string(data)
	for _, keyword := range []string{"status", "constraint", "dispatch", "pause", "resume", "help"} {
		if !strings.Contains(text, keyword) {
			t.Errorf("help blocks missing keyword %q", keyword)
		}
	}
}

// TestSlackButton verifies button element structure.
func TestSlackButton(t *testing.T) {
	btn := slackButton("action_id_1", "action_id_1", "Click Me", "primary")
	if btn["type"] != "button" {
		t.Errorf("expected type=button, got %v", btn["type"])
	}
	if btn["action_id"] != "action_id_1" {
		t.Errorf("expected action_id=action_id_1, got %v", btn["action_id"])
	}
	if btn["style"] != "primary" {
		t.Errorf("expected style=primary, got %v", btn["style"])
	}
}

// TestSlackButton_DefaultStyleOmitted verifies "default" style is not serialised.
func TestSlackButton_DefaultStyleOmitted(t *testing.T) {
	btn := slackButton("act", "act", "Default", "")
	if _, ok := btn["style"]; ok {
		t.Error("empty style should be omitted from button element")
	}
}

// TestSlackTextBlocks verifies slackTextBlocks wraps text in a section block.
func TestSlackTextBlocks(t *testing.T) {
	blocks := slackTextBlocks("hello")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	data, _ := json.Marshal(blocks[0])
	if !strings.Contains(string(data), "hello") {
		t.Errorf("expected text 'hello' in block, got %s", data)
	}
}

// TestSlackEventHandler_PostViaAPI_Success verifies postViaAPI calls chat.postMessage correctly.
func TestSlackEventHandler_PostViaAPI_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat.postMessage" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing or wrong Authorization header")
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["channel"] != "C123" {
			t.Errorf("expected channel C123, got %v", body["channel"])
		}
		if body["thread_ts"] != "111.222" {
			t.Errorf("expected thread_ts 111.222, got %v", body["thread_ts"])
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":true}`)
	}))
	defer srv.Close()

	h := NewSlackEventHandler("", "test-token", nil)
	// Override the Slack API URL for testing by patching via a custom client pointed at the test server.
	h.client = &http.Client{
		Transport: &testURLRewriter{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	ctx := context.Background()
	blocks := slackTextBlocks("test message")
	if err := h.postViaAPI(ctx, "C123", "111.222", blocks); err != nil {
		t.Fatalf("postViaAPI: %v", err)
	}
}

// TestSlackEventHandler_PostViaAPI_APIError verifies API error propagation.
func TestSlackEventHandler_PostViaAPI_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":false,"error":"channel_not_found"}`)
	}))
	defer srv.Close()

	h := NewSlackEventHandler("", "tok", nil)
	h.client = &http.Client{
		Transport: &testURLRewriter{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	err := h.postViaAPI(context.Background(), "C999", "0", slackTextBlocks("x"))
	if err == nil {
		t.Fatal("expected error from API error response")
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("expected channel_not_found in error, got %v", err)
	}
}

// testURLRewriter rewrites requests to the Slack API domain to a test server.
type testURLRewriter struct {
	base string
	rt   http.RoundTripper
}

func (t *testURLRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite https://slack.com/api/... to test server
	req2 := req.Clone(req.Context())
	req2.URL.Host = strings.TrimPrefix(t.base, "http://")
	req2.URL.Scheme = "http"
	rt := t.rt
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(req2)
}

// TestWebhookServer_SetSlackEvents verifies the /slack/events route is registered.
func TestWebhookServer_SetSlackEvents(t *testing.T) {
	// Create a minimal dispatcher to satisfy constructor requirements.
	ws := &WebhookServer{mux: http.NewServeMux()}
	handler := newTestSlackHandler()
	ws.SetSlackEvents(handler)

	if ws.SlackEvents() != handler {
		t.Fatal("SlackEvents() should return the registered handler")
	}

	// Verify the endpoint exists (should get 405 for GET, not 404).
	req := httptest.NewRequest(http.MethodGet, "/slack/events", nil)
	w := httptest.NewRecorder()
	ws.mux.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Error("expected /slack/events to be registered, got 404")
	}
}
