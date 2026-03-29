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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
)

// WebhookServer is a lightweight HTTP server for receiving GitHub webhooks
// and Slack interactive component callbacks.
// It replaces webhook-listener.py with coordinated dispatch.
type WebhookServer struct {
	dispatcher   *Dispatcher
	secret       []byte
	mux          *http.ServeMux
	sprintStore  *sprint.Store
	benchmark    *BenchmarkTracker
	slackActions *SlackInteractionHandler
}

// NewWebhookServer creates a webhook handler backed by the dispatcher.
// If secretFile is empty, it defaults to ~/.agentguard/webhook-secret.
func NewWebhookServer(dispatcher *Dispatcher, secretFile string) *WebhookServer {
	if secretFile == "" {
		home, _ := os.UserHomeDir()
		secretFile = filepath.Join(home, ".agentguard", "webhook-secret")
	}

	var secret []byte
	if data, err := os.ReadFile(secretFile); err == nil {
		secret = []byte(strings.TrimSpace(string(data)))
	}

	ws := &WebhookServer{
		dispatcher: dispatcher,
		secret:     secret,
		mux:        http.NewServeMux(),
	}
	ws.mux.HandleFunc("/webhook", ws.handleWebhook)
	ws.mux.HandleFunc("/health", ws.handleHealth)
	ws.mux.HandleFunc("/dispatch/status", ws.handleStatus)
	ws.mux.HandleFunc("/dispatch/trigger", ws.handleTrigger)
	ws.mux.HandleFunc("/dispatch/timer", ws.handleTimerTrigger)
	ws.mux.HandleFunc("/sprint/status", ws.handleSprintStatus)
	ws.mux.HandleFunc("/sprint/sync", ws.handleSprintSync)
	ws.mux.HandleFunc("/benchmark", ws.handleBenchmark)
	return ws
}

// SetSprintStore enables sprint HTTP endpoints.
func (ws *WebhookServer) SetSprintStore(s *sprint.Store) {
	ws.sprintStore = s
}

// SetBenchmark enables benchmark HTTP endpoints.
func (ws *WebhookServer) SetBenchmark(bt *BenchmarkTracker) {
	ws.benchmark = bt
}

// SetSlackInteractions enables the /slack/actions endpoint for handling
// Slack button callbacks. Call this after creating the WebhookServer when
// SLACK_SIGNING_SECRET is available.
func (ws *WebhookServer) SetSlackInteractions(handler *SlackInteractionHandler) {
	ws.slackActions = handler
	ws.mux.HandleFunc("/slack/actions", ws.handleSlackActions)
}

// ServeHTTP implements the http.Handler interface.
func (ws *WebhookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws.mux.ServeHTTP(w, r)
}

// ListenAndServe starts the webhook server on the given address.
func (ws *WebhookServer) ListenAndServe(addr string) error {
	server := &http.Server{
		Addr:         addr,
		Handler:      ws,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return server.ListenAndServe()
}

func (ws *WebhookServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "octi-pulpo"})
}

func (ws *WebhookServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Verify HMAC-SHA256 signature if secret is configured
	if len(ws.secret) > 0 {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !ws.verifySignature(body, sig) {
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}
	}

	// Parse the GitHub payload
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	githubEvent := r.Header.Get("X-GitHub-Event")
	action := getString(payload, "action")
	repo := getNestedString(payload, "repository", "full_name")

	// Convert GitHub event to our Event type
	event := ws.parseGitHubEvent(githubEvent, action, repo, payload)
	if event == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "action": "ignored"})
		return
	}

	// Dispatch through the coordinator
	ctx := context.Background()
	results, err := ws.dispatcher.DispatchEvent(ctx, *event)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "dispatched": results})
}

func (ws *WebhookServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()
	depth, _ := ws.dispatcher.PendingCount(ctx)
	agents, _ := ws.dispatcher.PendingAgents(ctx)
	recent, _ := ws.dispatcher.RecentDispatches(ctx, 20)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"queue_depth":      depth,
		"pending_agents":   agents,
		"recent_dispatches": recent,
	})
}

func (ws *WebhookServer) handleTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Agent    string `json:"agent"`
		Priority int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Agent == "" {
		http.Error(w, "agent name required", http.StatusBadRequest)
		return
	}

	event := Event{
		Type:     EventManual,
		Source:   "http",
		Priority: req.Priority,
		Payload:  map[string]string{"triggered_by": "http_api"},
	}

	ctx := context.Background()
	result, err := ws.dispatcher.Dispatch(ctx, event, req.Agent, req.Priority)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (ws *WebhookServer) handleTimerTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Agent    string `json:"agent"`
		Priority int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Agent == "" {
		http.Error(w, "agent name required", http.StatusBadRequest)
		return
	}
	if req.Priority == 0 {
		req.Priority = 2 // default timer priority = normal
	}

	event := Event{
		Type:     EventTimer,
		Source:   "timer",
		Priority: req.Priority,
		Payload:  map[string]string{"triggered_by": "octi-timer"},
	}

	ctx := context.Background()
	result, err := ws.dispatcher.Dispatch(ctx, event, req.Agent, req.Priority)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (ws *WebhookServer) handleSprintStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.sprintStore == nil {
		http.Error(w, "sprint store not initialized", http.StatusServiceUnavailable)
		return
	}

	ctx := context.Background()
	items, err := ws.sprintStore.GetAll(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Group by squad
	grouped := make(map[string][]sprint.SprintItem)
	for _, item := range items {
		grouped[item.Squad] = append(grouped[item.Squad], item)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(grouped)
}

func (ws *WebhookServer) handleSprintSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.sprintStore == nil {
		http.Error(w, "sprint store not initialized", http.StatusServiceUnavailable)
		return
	}

	ctx := context.Background()
	var results []map[string]string
	for _, repo := range sprint.DefaultRepos {
		entry := map[string]string{"repo": repo}
		if err := ws.sprintStore.Sync(ctx, repo); err != nil {
			entry["status"] = "error"
			entry["error"] = err.Error()
		} else {
			entry["status"] = "synced"
		}
		results = append(results, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"results": results})
}

func (ws *WebhookServer) handleBenchmark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.benchmark == nil {
		http.Error(w, "benchmark tracker not initialized", http.StatusServiceUnavailable)
		return
	}

	ctx := context.Background()
	metrics, err := ws.benchmark.Compute(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func (ws *WebhookServer) parseGitHubEvent(eventType, action, repo string, payload map[string]interface{}) *Event {
	switch eventType {
	case "pull_request":
		switch action {
		case "opened", "ready_for_review":
			return &Event{
				Type:     EventPROpened,
				Source:   "github",
				Repo:     repo,
				Priority: 1,
				Payload: map[string]string{
					"action":    action,
					"pr_number": fmt.Sprintf("%.0f", getNestedNumber(payload, "pull_request", "number")),
				},
			}
		case "synchronize":
			return &Event{
				Type:     EventPRUpdated,
				Source:   "github",
				Repo:     repo,
				Priority: 1,
				Payload: map[string]string{
					"action":    action,
					"pr_number": fmt.Sprintf("%.0f", getNestedNumber(payload, "pull_request", "number")),
				},
			}
		}

	case "check_suite", "check_run":
		if action == "completed" {
			return &Event{
				Type:     EventCICompleted,
				Source:   "github",
				Repo:     repo,
				Priority: 2,
				Payload: map[string]string{
					"event_type": eventType,
					"action":     action,
				},
			}
		}
	}

	return nil // unrecognized event
}

// handleSlackActions handles POST /slack/actions — Slack interactive component callbacks.
// Slack sends a URL-encoded form body with a "payload" field when a user clicks a button.
// The handler verifies the X-Slack-Signature, dispatches the action, and returns an
// ephemeral message confirming the action to the user in Slack.
func (ws *WebhookServer) handleSlackActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.slackActions == nil {
		http.Error(w, "slack interactions not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := context.Background()
	msg, err := ws.slackActions.Handle(ctx, r)
	if err != nil {
		// Log the real error but don't leak internals to Slack
		fmt.Fprintf(os.Stderr, "slack action error: %v\n", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"response_type": "ephemeral",
		"text":          msg,
	})
}

func (ws *WebhookServer) verifySignature(payload []byte, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, ws.secret)
	mac.Write(payload)
	expected := mac.Sum(nil)
	return hmac.Equal(sigBytes, expected)
}

// JSON helper functions for navigating untyped webhook payloads.

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getNestedString(m map[string]interface{}, keys ...string) string {
	current := m
	for i, key := range keys {
		v, ok := current[key]
		if !ok {
			return ""
		}
		if i == len(keys)-1 {
			if s, ok := v.(string); ok {
				return s
			}
			return ""
		}
		if next, ok := v.(map[string]interface{}); ok {
			current = next
		} else {
			return ""
		}
	}
	return ""
}

func getNestedNumber(m map[string]interface{}, keys ...string) float64 {
	current := m
	for i, key := range keys {
		v, ok := current[key]
		if !ok {
			return 0
		}
		if i == len(keys)-1 {
			if n, ok := v.(float64); ok {
				return n
			}
			return 0
		}
		if next, ok := v.(map[string]interface{}); ok {
			current = next
		} else {
			return 0
		}
	}
	return 0
}
