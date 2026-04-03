package dispatch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/budget"
	"github.com/AgentGuardHQ/octi-pulpo/internal/memory"
	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
)

// WebhookServer is a lightweight HTTP server for receiving GitHub webhooks
// and Slack interactive action callbacks.
// It replaces webhook-listener.py with coordinated dispatch.
type WebhookServer struct {
	dispatcher         *Dispatcher
	secret             []byte // GitHub HMAC-SHA256 webhook secret
	slackSigningSecret []byte // Slack signing secret for action callbacks
	mux                *http.ServeMux
	sprintStore        *sprint.Store
	benchmark          *BenchmarkTracker
	slackEvents        *SlackEventHandler
	budgetStore        *budget.BudgetStore
	memoryStore        *memory.Store
	triageHandler      *TriageHandler
	reviewHandler      *ReviewHandler
	plannerHandler     *PlannerHandler
	cascadeHandler     *CascadeHandler
	draftConverter     *DraftConverter
}

// SetTriageHandler enables automatic issue triage via Claude API.
func (ws *WebhookServer) SetTriageHandler(th *TriageHandler) {
	ws.triageHandler = th
}

// SetReviewHandler enables automatic PR review + merge via Claude API.
func (ws *WebhookServer) SetReviewHandler(rh *ReviewHandler) {
	ws.reviewHandler = rh
}

// SetPlannerHandler enables automatic issue scoping for tier:b-scope issues.
func (ws *WebhookServer) SetPlannerHandler(ph *PlannerHandler) {
	ws.plannerHandler = ph
}

// SetCascadeHandler enables strategy cascade — syncs roadmap to issues across repos.
func (ws *WebhookServer) SetCascadeHandler(ch *CascadeHandler) {
	ws.cascadeHandler = ch
}

// SetDraftConverter enables automatic draft-to-ready conversion for Copilot PRs.
func (ws *WebhookServer) SetDraftConverter(dc *DraftConverter) {
	ws.draftConverter = dc
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
	ws.mux.HandleFunc("/slack/actions", ws.handleSlackActions)
	ws.mux.HandleFunc("/api/memory", ws.handleMemoryStore)
	ws.mux.HandleFunc("/cascade/trigger", ws.handleCascadeTrigger)
	return ws
}

// SetMemoryStore enables the /api/memory endpoint for CLI session telemetry.
func (ws *WebhookServer) SetMemoryStore(m *memory.Store) {
	ws.memoryStore = m
}

// handleMemoryStore receives memory entries from AgentGuard CLI hooks
// via the Octi Bridge. This is how human CLI sessions feed the swarm's
// episodic memory.
func (ws *WebhookServer) handleMemoryStore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if ws.memoryStore == nil {
		http.Error(w, "memory store not configured", http.StatusServiceUnavailable)
		return
	}

	var payload struct {
		Content string   `json:"content"`
		Topics  []string `json:"topics"`
		AgentID string   `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if payload.Content == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}

	agentID := payload.AgentID
	if agentID == "" {
		agentID = "cli-bridge"
	}

	id, err := ws.memoryStore.Put(r.Context(), agentID, payload.Content, payload.Topics)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "stored"})
}

// SetSprintStore enables sprint HTTP endpoints.
func (ws *WebhookServer) SetSprintStore(s *sprint.Store) {
	ws.sprintStore = s
}

// SetBenchmark enables benchmark HTTP endpoints.
func (ws *WebhookServer) SetBenchmark(bt *BenchmarkTracker) {
	ws.benchmark = bt
}

// SetSlackEvents registers a SlackEventHandler on the /slack/events endpoint.
// Call after NewWebhookServer; the route is registered lazily on first call.
func (ws *WebhookServer) SetSlackEvents(h *SlackEventHandler) {
	ws.slackEvents = h
	ws.mux.HandleFunc("/slack/events", h.Handle)
}

// SlackEvents returns the registered SlackEventHandler, or nil if not set.
func (ws *WebhookServer) SlackEvents() *SlackEventHandler {
	return ws.slackEvents
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

	// Draft-to-ready gate: detect when Copilot signals review_requested on a draft PR
	// and promote it to ready-for-review so normal CI and review pipelines can fire.
	if githubEvent == "pull_request" && action == "review_requested" && ws.draftConverter != nil {
		prNumber := int(getNestedNumber(payload, "pull_request", "number"))
		author := getNestedString(payload, "pull_request", "user", "login")
		title := getNestedString(payload, "pull_request", "title")
		isDraft := getNestedBool(payload, "pull_request", "draft")

		if ShouldConvert(author, title, isDraft, action) {
			go func() {
				result, err := ws.draftConverter.ConvertToReady(context.Background(), repo, prNumber)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] draft-convert error PR #%d: %v\n", prNumber, err)
				} else {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] draft-convert PR #%d: converted=%v reason=%s\n",
						prNumber, result.Converted, result.Reason)
				}
			}()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":     true,
				"action": "draft_convert_dispatched",
			})
			return
		}
	}

	// Convert GitHub event to our Event type
	event := ws.parseGitHubEvent(githubEvent, action, repo, payload)
	if event == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "action": "ignored"})
		return
	}

	ctx := context.Background()

	// Issue triage: classify via Claude API on this box, then label
	if event.Type == EventIssueOpened && ws.triageHandler != nil {
		issueNumber := int(getNestedNumber(payload, "issue", "number"))
		title := getNestedString(payload, "issue", "title")
		issueBody := getNestedString(payload, "issue", "body")
		var issueLabels []string
		if labelsRaw, ok := payload["issue"].(map[string]interface{}); ok {
			if arr, ok := labelsRaw["labels"].([]interface{}); ok {
				for _, l := range arr {
					if lm, ok := l.(map[string]interface{}); ok {
						if name, ok := lm["name"].(string); ok {
							issueLabels = append(issueLabels, name)
						}
					}
				}
			}
		}

		triageResult, triageErr := ws.triageHandler.HandleIssue(ctx, repo, issueNumber, title, issueBody, issueLabels)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":      true,
			"action":  "triaged",
			"triage":  triageResult,
			"error":   fmt.Sprintf("%v", triageErr),
		})
		return
	}

	// Planner: when tier:b-scope label is applied, scope the issue via Claude API
	if event.Type == EventIssueLabeled && ws.plannerHandler != nil {
		labelName := event.Payload["label"]
		if labelName == "tier:b-scope" {
			issueNumber := int(getNestedNumber(payload, "issue", "number"))
			title := getNestedString(payload, "issue", "title")
			issueBody := getNestedString(payload, "issue", "body")
			go func() {
				result, err := ws.plannerHandler.HandleIssue(context.Background(), repo, issueNumber, title, issueBody)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] planner error #%d: %v\n", issueNumber, err)
				} else {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] planner #%d: escalate=%v subs=%d reason=%s\n",
						issueNumber, result.Escalate, len(result.SubIssues), result.Reason)
				}
			}()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":     true,
				"action": "planner_dispatched",
			})
			return
		}
	}

	// Auto-trigger PR gate for Copilot PRs via workflow_dispatch.
	// GitHub blocks Copilot's pull_request-triggered workflows with action_required.
	// By triggering via workflow_dispatch using our token, the run is attributed to us, not Copilot.
	if (event.Type == EventPROpened || event.Type == EventPRUpdated) && ws.triageHandler != nil {
		prAuthor := getNestedString(payload, "pull_request", "user", "login")
		if strings.Contains(strings.ToLower(prAuthor), "copilot") {
			prNumber := event.Payload["pr_number"]
			go func() {
				err := ws.triggerWorkflow(context.Background(), repo, "octi-pr-gate.yml", prNumber)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] gate trigger error PR #%s: %v\n", prNumber, err)
				} else {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] gate triggered for Copilot PR #%s in %s\n", prNumber, repo)
				}
			}()
		}
	}

	// PR review: Claude reviews on both tier:review (first pass) and tier:b-code (escalation).
	// On tier:review: reviews + approves/merges (closes the autonomy loop).
	// On tier:b-code: senior escalation after Copilot fix loop exhausted.
	if event.Type == EventPRLabeled && ws.reviewHandler != nil {
		labelName := event.Payload["label"]
		if labelName == "tier:review" || labelName == "tier:b-code" {
			prNumber := int(getNestedNumber(payload, "pull_request", "number"))
			go func() {
				reviewResult, reviewErr := ws.reviewHandler.HandlePR(context.Background(), repo, prNumber)
				if reviewErr != nil {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] review error PR #%d: %v\n", prNumber, reviewErr)
				} else {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] review PR #%d: %s (confidence=%.2f, merged=%v)\n",
						prNumber, reviewResult.Decision, reviewResult.Confidence, reviewResult.Merged)
				}
			}()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":     true,
				"action": "senior_review_dispatched",
			})
			return
		}
	}

	// Strategy cascade: when roadmap.md or strategy/ changes are pushed to
	// agentguard-workspace, diff roadmap against managed issues and sync.
	if event.Type == EventPush && ws.cascadeHandler != nil {
		if repo == "AgentGuardHQ/agentguard-workspace" && event.Payload["touches_roadmap"] == "true" {
			go func() {
				result, err := ws.cascadeHandler.HandlePush(context.Background())
				if err != nil {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] cascade error: %v\n", err)
				} else {
					fmt.Fprintf(os.Stderr, "[octi-pulpo] cascade: created=%d closed=%d relabeled=%d\n",
						result.Created, result.Closed, result.Relabeled)
				}
			}()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":     true,
				"action": "cascade_dispatched",
			})
			return
		}
	}

	// Dispatch through the coordinator
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

// handleCascadeTrigger allows manual triggering of the strategy cascade.
// POST /cascade/trigger — no body needed.
func (ws *WebhookServer) handleCascadeTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ws.cascadeHandler == nil {
		http.Error(w, "cascade handler not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	result, err := ws.cascadeHandler.HandlePush(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// SetSlackSigningSecret configures the Slack signing secret used to verify
// interactive action callbacks at /slack/actions.
func (ws *WebhookServer) SetSlackSigningSecret(secret []byte) {
	ws.slackSigningSecret = secret
}

// SetBudgetStore enables budget override actions in /slack/actions.
func (ws *WebhookServer) SetBudgetStore(bs *budget.BudgetStore) {
	ws.budgetStore = bs
}

// handleSlackActions receives interactive action callbacks from Slack (Block Kit buttons).
// Slack POSTs application/x-www-form-urlencoded with a "payload" field containing JSON.
//
// Supported action_ids and their effects:
//   - pause_squad    — publishes a "pause-squad:<driver>" coord signal to Redis
//   - switch_tier    — dispatches the routing recalculation agent
//   - ignore_alert   — no-op, acknowledges the alert
//   - merge_pr       — triggers pr-merger-agent for the given repo/pr
//   - review_pr      — no-op, acknowledges
//   - skip_pr        — no-op, acknowledges
//   - accept_goal    — publishes a "goal-accepted:<squad>" coord signal
//   - request_changes — publishes a "goal-rejected:<squad>" coord signal
func (ws *WebhookServer) handleSlackActions(w http.ResponseWriter, r *http.Request) {
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

	// Verify Slack request signature when signing secret is configured.
	if len(ws.slackSigningSecret) > 0 {
		ts := r.Header.Get("X-Slack-Request-Timestamp")
		sig := r.Header.Get("X-Slack-Signature")
		if !ws.verifySlackSignature(body, ts, sig) {
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}
	}

	// Slack sends payload as URL-encoded form: payload=<json>
	values, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "invalid form body", http.StatusBadRequest)
		return
	}
	rawPayload := values.Get("payload")
	if rawPayload == "" {
		http.Error(w, "missing payload field", http.StatusBadRequest)
		return
	}

	var slackPayload struct {
		Type    string `json:"type"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
		ResponseURL string `json:"response_url"`
		User        struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(rawPayload), &slackPayload); err != nil {
		http.Error(w, "invalid payload JSON", http.StatusBadRequest)
		return
	}
	if len(slackPayload.Actions) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	action := slackPayload.Actions[0]
	ctx := r.Context()
	actor := slackPayload.User.Name

	ack, err := ws.routeSlackAction(ctx, action.ActionID, action.Value, actor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Respond immediately to Slack with an acknowledgement message.
	// This replaces the original interactive message so it can't be double-clicked.
	if slackPayload.ResponseURL != "" {
		go ws.updateSlackMessage(slackPayload.ResponseURL, ack)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"text": ack})
}

// routeSlackAction performs the work triggered by a Slack button click.
// It returns a human-readable acknowledgement string for the Slack message update.
func (ws *WebhookServer) routeSlackAction(ctx context.Context, actionID, value, actor string) (string, error) {
	switch actionID {
	case "pause_squad":
		// Publish a pause signal to Redis for this driver.
		ch := ws.dispatcher.Namespace() + ":signal-stream"
		sig := fmt.Sprintf(`{"agent_id":"slack:%s","type":"directive","payload":"pause-squad:%s","timestamp":"%s"}`,
			actor, value, time.Now().UTC().Format(time.RFC3339))
		if err := ws.dispatcher.RedisClient().Publish(ctx, ch, sig).Err(); err != nil {
			return "", fmt.Errorf("publish pause signal: %w", err)
		}
		return fmt.Sprintf("⏸ Squad paused for driver `%s` by @%s", value, actor), nil

	case "switch_tier":
		// Trigger the routing recalculation by dispatching the senior agent.
		event := Event{
			Type:    EventSignal,
			Source:  "slack",
			Payload: map[string]string{"action": "switch_tier", "driver": value, "actor": actor},
		}
		_, err := ws.dispatcher.Dispatch(ctx, event, "octi-pulpo-sr", 1)
		if err != nil {
			return "", fmt.Errorf("dispatch switch-tier: %w", err)
		}
		return fmt.Sprintf("🔀 Tier switch initiated for driver `%s` by @%s", value, actor), nil

	case "merge_pr":
		// Trigger the PR merger agent.
		event := Event{
			Type:    EventSignal,
			Source:  "slack",
			Payload: map[string]string{"action": "merge_pr", "pr": value, "actor": actor},
		}
		_, err := ws.dispatcher.Dispatch(ctx, event, "pr-merger-agent", 1)
		if err != nil {
			return "", fmt.Errorf("dispatch merge: %w", err)
		}
		return fmt.Sprintf("🔀 Merge triggered for PR `%s` by @%s", value, actor), nil

	case "accept_goal":
		ch := ws.dispatcher.Namespace() + ":signal-stream"
		sig := fmt.Sprintf(`{"agent_id":"slack:%s","type":"directive","payload":"goal-accepted:%s","timestamp":"%s"}`,
			actor, value, time.Now().UTC().Format(time.RFC3339))
		if err := ws.dispatcher.RedisClient().Publish(ctx, ch, sig).Err(); err != nil {
			return "", fmt.Errorf("publish goal-accepted signal: %w", err)
		}
		return fmt.Sprintf("✅ Sprint goal accepted for `%s` by @%s", value, actor), nil

	case "request_changes":
		ch := ws.dispatcher.Namespace() + ":signal-stream"
		sig := fmt.Sprintf(`{"agent_id":"slack:%s","type":"directive","payload":"goal-rejected:%s","timestamp":"%s"}`,
			actor, value, time.Now().UTC().Format(time.RFC3339))
		if err := ws.dispatcher.RedisClient().Publish(ctx, ch, sig).Err(); err != nil {
			return "", fmt.Errorf("publish goal-rejected signal: %w", err)
		}
		return fmt.Sprintf("🔄 Changes requested for `%s` by @%s", value, actor), nil

	case "override_budget":
		// Unpause a budget-exhausted agent.
		if ws.budgetStore == nil {
			return "", fmt.Errorf("budget store not configured")
		}
		if err := ws.budgetStore.Unpause(ctx, value); err != nil {
			return "", fmt.Errorf("unpause budget for %s: %w", value, err)
		}
		return fmt.Sprintf("✅ Budget override — `%s` unpaused by @%s", value, actor), nil

	case "dismiss_budget_alert":
		// Acknowledged — agent stays paused.
		return fmt.Sprintf("👍 Budget alert dismissed by @%s — `%s` remains paused", actor, value), nil

	case "ignore_alert", "review_pr", "skip_pr":
		// Acknowledged — no further action needed.
		return fmt.Sprintf("👍 Acknowledged by @%s", actor), nil

	default:
		return fmt.Sprintf("⚠️ Unknown action `%s` by @%s", actionID, actor), nil
	}
}

// updateSlackMessage POSTs an updated message to Slack's response_url to replace
// the original interactive message with an acknowledgement. Fire-and-forget.
func (ws *WebhookServer) updateSlackMessage(responseURL, text string) {
	payload, _ := json.Marshal(map[string]interface{}{
		"replace_original": true,
		"text":             text,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// verifySlackSignature validates a Slack request using the v0 signing scheme:
//
//	sig_basestring = "v0:" + timestamp + ":" + body
//	expected = "v0=" + hex(hmac-sha256(signing_secret, sig_basestring))
func (ws *WebhookServer) verifySlackSignature(body []byte, timestamp, signature string) bool {
	if !strings.HasPrefix(signature, "v0=") {
		return false
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "v0="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, ws.slackSigningSecret)
	fmt.Fprintf(mac, "v0:%s:", timestamp)
	mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(sigBytes, expected)
}

func (ws *WebhookServer) parseGitHubEvent(eventType, action, repo string, payload map[string]interface{}) *Event {
	switch eventType {
	case "issues":
		switch action {
		case "opened":
			issueNumber := getNestedNumber(payload, "issue", "number")
			return &Event{
				Type:     EventIssueOpened,
				Source:   "github",
				Repo:     repo,
				Priority: 2,
				Payload: map[string]string{
					"action":       action,
					"issue_number": fmt.Sprintf("%.0f", issueNumber),
					"title":        getNestedString(payload, "issue", "title"),
					"body":         getNestedString(payload, "issue", "body"),
				},
			}
		case "labeled":
			labelName := getNestedString(payload, "label", "name")
			return &Event{
				Type:     EventIssueLabeled,
				Source:   "github",
				Repo:     repo,
				Priority: 2,
				Payload: map[string]string{
					"action":       action,
					"issue_number": fmt.Sprintf("%.0f", getNestedNumber(payload, "issue", "number")),
					"title":        getNestedString(payload, "issue", "title"),
					"body":         getNestedString(payload, "issue", "body"),
					"label":        labelName,
				},
			}
		}

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
		case "labeled":
			labelName := getNestedString(payload, "label", "name")
			return &Event{
				Type:     EventPRLabeled,
				Source:   "github",
				Repo:     repo,
				Priority: 1,
				Payload: map[string]string{
					"action":    action,
					"pr_number": fmt.Sprintf("%.0f", getNestedNumber(payload, "pull_request", "number")),
					"label":     labelName,
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

	case "push":
		// Check if any commit touches roadmap.md or strategy/ files
		touchesRoadmap := "false"
		if commits, ok := payload["commits"].([]interface{}); ok {
			for _, c := range commits {
				cm, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				for _, fileKey := range []string{"added", "modified", "removed"} {
					if files, ok := cm[fileKey].([]interface{}); ok {
						for _, f := range files {
							if fname, ok := f.(string); ok {
								if fname == "roadmap.md" || strings.HasPrefix(fname, "strategy/") {
									touchesRoadmap = "true"
								}
							}
						}
					}
				}
			}
		}
		ref := getString(payload, "ref")
		return &Event{
			Type:     EventPush,
			Source:   "github",
			Repo:     repo,
			Priority: 2,
			Payload: map[string]string{
				"ref":             ref,
				"touches_roadmap": touchesRoadmap,
			},
		}
	}

	return nil // unrecognized event
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

// triggerWorkflow fires a workflow_dispatch event for a workflow in the given repo.
// Used to bypass action_required blocks on Copilot PRs — the dispatch runs as our token owner.
func (ws *WebhookServer) triggerWorkflow(ctx context.Context, repo, workflowFile, prNumber string) error {
	ghToken := os.Getenv("GITHUB_TOKEN")
	if ghToken == "" {
		return fmt.Errorf("GITHUB_TOKEN not set")
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/workflows/%s/dispatches", repo, workflowFile)
	body, _ := json.Marshal(map[string]interface{}{
		"ref":    "main",
		"inputs": map[string]string{"pr_number": prNumber},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dispatch API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
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

func getNestedBool(m map[string]interface{}, keys ...string) bool {
	current := m
	for i, key := range keys {
		v, ok := current[key]
		if !ok {
			return false
		}
		if i == len(keys)-1 {
			if b, ok := v.(bool); ok {
				return b
			}
			return false
		}
		if next, ok := v.(map[string]interface{}); ok {
			current = next
		} else {
			return false
		}
	}
	return false
}
