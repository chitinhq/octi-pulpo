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
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/budget"
	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
)

// slackEventEnvelope is the outer Slack Events API payload.
type slackEventEnvelope struct {
	Type      string        `json:"type"`
	Challenge string        `json:"challenge"`
	Event     slackMsgEvent `json:"event"`
}

// slackMsgEvent is a Slack message event (type=message).
type slackMsgEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Text    string `json:"text"`
	User    string `json:"user"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	BotID   string `json:"bot_id"`  // set on bot-posted messages
	AppID   string `json:"app_id"`  // set on app-posted messages
}

// SlackEventHandler processes Slack Events API webhooks and routes keyword
// commands to swarm actions: status, constraint, dispatch, pause, resume.
//
// Wire it with:
//
//	handler := dispatch.NewSlackEventHandler(signingSecret, botToken, dispatcher)
//	handler.SetSprintStore(sprintStore)
//	handler.SetBenchmark(benchmark)
//	handler.SetNotifier(notifier)
//	handler.SetBrain(brain)
//	webhookServer.SetSlackEvents(handler)
//
// Required env vars:
//
//	SLACK_SIGNING_SECRET — verify Events API payloads
//	SLACK_BOT_TOKEN      — post replies via chat.postMessage
type SlackEventHandler struct {
	signingSecret string
	botToken      string
	dispatcher    *Dispatcher
	sprintStore   *sprint.Store
	benchmark     *BenchmarkTracker
	notifier      *Notifier
	brain         *Brain
	budgetStore   *budget.BudgetStore
	log           *log.Logger
	client        *http.Client
}

// NewSlackEventHandler creates a handler for the Slack Events API.
// If signingSecret is empty, signature verification is skipped (dev mode).
// If botToken is empty, replies fall back to the notifier's webhook URL.
func NewSlackEventHandler(signingSecret, botToken string, dispatcher *Dispatcher) *SlackEventHandler {
	return &SlackEventHandler{
		signingSecret: signingSecret,
		botToken:      botToken,
		dispatcher:    dispatcher,
		log:           log.New(os.Stderr, "[slack-events] ", log.LstdFlags),
		client:        &http.Client{Timeout: 10 * time.Second},
	}
}

// SetSprintStore enables sprint-aware status replies.
func (h *SlackEventHandler) SetSprintStore(s *sprint.Store) { h.sprintStore = s }

// SetBenchmark enables benchmark metrics in status replies.
func (h *SlackEventHandler) SetBenchmark(bt *BenchmarkTracker) { h.benchmark = bt }

// SetNotifier enables incoming-webhook fallback for replies (when no bot token).
func (h *SlackEventHandler) SetNotifier(n *Notifier) { h.notifier = n }

// SetBrain enables constraint-aware replies.
func (h *SlackEventHandler) SetBrain(b *Brain) { h.brain = b }

// SetBudgetStore enables budget override commands.
func (h *SlackEventHandler) SetBudgetStore(bs *budget.BudgetStore) { h.budgetStore = bs }

// Handle processes a Slack Events API HTTP request.
// It ACKs immediately and dispatches command handling in a goroutine
// to satisfy Slack's 3-second response window.
func (h *SlackEventHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if !h.verifySignature(r, body) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}

	var env slackEventEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// URL verification challenge — one-time during Slack app setup.
	if env.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"challenge": env.Challenge})
		return
	}

	// ACK immediately; Slack requires < 3s.
	w.WriteHeader(http.StatusOK)

	if env.Type != "event_callback" {
		return
	}

	msg := env.Event
	// Skip bot/app messages and non-message events to prevent loops.
	if msg.BotID != "" || msg.AppID != "" || msg.Type != "message" || msg.Subtype != "" || msg.Text == "" {
		return
	}

	cmd, args := h.parseCommand(msg.Text)
	if cmd == "" {
		return
	}

	go func() {
		ctx := context.Background()
		h.handleCommand(ctx, cmd, args, msg.Channel, msg.TS)
	}()
}

// parseCommand extracts a command keyword and optional args from Slack text.
// It strips @mentions and normalises to lowercase.
// Returns ("", "") if no command is recognised.
func (h *SlackEventHandler) parseCommand(text string) (cmd, args string) {
	// Strip <@USERID> and <@USERID|name> mentions
	for strings.Contains(text, "<@") {
		start := strings.Index(text, "<@")
		end := strings.Index(text[start:], ">")
		if end < 0 {
			break
		}
		text = text[:start] + text[start+end+1:]
	}
	text = strings.TrimSpace(strings.ToLower(text))

	switch {
	case text == "status",
		strings.HasPrefix(text, "what's the status"),
		strings.HasPrefix(text, "whats the status"):
		return "status", ""
	case text == "constraint",
		strings.HasPrefix(text, "what's the constraint"),
		strings.HasPrefix(text, "whats the constraint"):
		return "constraint", ""
	case text == "help", text == "?", text == "commands":
		return "help", ""
	case strings.HasPrefix(text, "dispatch "):
		return "dispatch", strings.TrimSpace(strings.TrimPrefix(text, "dispatch "))
	case strings.HasPrefix(text, "trigger "):
		return "dispatch", strings.TrimSpace(strings.TrimPrefix(text, "trigger "))
	case strings.HasPrefix(text, "pause "):
		return "pause", strings.TrimSpace(strings.TrimPrefix(text, "pause "))
	case strings.HasPrefix(text, "resume "):
		return "resume", strings.TrimSpace(strings.TrimPrefix(text, "resume "))
	case strings.HasPrefix(text, "budget override "):
		return "budget_override", strings.TrimSpace(strings.TrimPrefix(text, "budget override "))
	}
	return "", ""
}

// handleCommand executes the recognised command and posts a reply.
func (h *SlackEventHandler) handleCommand(ctx context.Context, cmd, args, channel, ts string) {
	var blocks []interface{}
	switch cmd {
	case "status":
		blocks = h.buildStatusBlocks(ctx)
	case "constraint":
		blocks = h.buildConstraintBlocks(ctx)
	case "dispatch":
		blocks = h.buildDispatchBlocks(ctx, args)
	case "pause":
		blocks = h.buildPauseBlocks(ctx, args)
	case "resume":
		blocks = h.buildResumeBlocks(ctx, args)
	case "budget_override":
		blocks = h.buildBudgetOverrideBlocks(ctx, args)
	case "help":
		blocks = h.buildHelpBlocks()
	default:
		return
	}

	if err := h.postReply(ctx, channel, ts, blocks); err != nil {
		h.log.Printf("post reply [%s]: %v", cmd, err)
	}
}

// buildStatusBlocks returns Block Kit blocks for a swarm status summary.
func (h *SlackEventHandler) buildStatusBlocks(ctx context.Context) []interface{} {
	var lines []string
	lines = append(lines, "*📊 Swarm Status*")

	depth, _ := h.dispatcher.PendingCount(ctx)
	agents, _ := h.dispatcher.PendingAgents(ctx)
	lines = append(lines, fmt.Sprintf("Queue depth: *%d* | Active agents: *%d*", depth, len(agents)))

	if h.benchmark != nil {
		if m, err := h.benchmark.Compute(ctx); err == nil {
			lines = append(lines, fmt.Sprintf(
				"Pass rate: *%.1f%%* | PRs/h: *%.1f* | Waste: *%.1f%%*",
				m.PassRate*100, m.PRsPerHour, m.WastePercent,
			))
		}
	}

	if h.sprintStore != nil {
		if items, err := h.sprintStore.GetAll(ctx); err == nil {
			var inProgress, blocked, done int
			for _, it := range items {
				switch it.Status {
				case "in_progress", "claimed":
					inProgress++
				case "blocked":
					blocked++
				case "done", "pr_open":
					done++
				}
			}
			lines = append(lines, fmt.Sprintf(
				"Sprint: *%d* in-progress | *%d* blocked | *%d* done",
				inProgress, blocked, done,
			))
		}
	}

	return []interface{}{
		slackSection(strings.Join(lines, "\n")),
		map[string]interface{}{
			"type": "actions",
			"elements": []interface{}{
				slackButton("view_status", "view_status", "View Details", "primary"),
				slackButton("view_benchmark", "view_benchmark", "View Benchmark", ""),
			},
		},
	}
}

// buildConstraintBlocks returns Block Kit blocks for the current system constraint.
func (h *SlackEventHandler) buildConstraintBlocks(ctx context.Context) []interface{} {
	var text string
	if h.brain != nil {
		c := h.brain.identifyConstraint(ctx)
		action := h.brain.highestLeverageAction(ctx, c)
		text = fmt.Sprintf("*🔍 Constraint:* `%s` (severity %d)\n_%s_", c.Type, c.Severity, c.Description)
		if action != nil {
			text += fmt.Sprintf("\n\n*Highest-leverage action:* dispatch `%s`", action.Agent)
			if action.IssueNum > 0 {
				text += fmt.Sprintf(" at #%d", action.IssueNum)
			}
			text += fmt.Sprintf(" (score: %.1f)\n_%s_", action.Score, action.Reason)
		}
	} else {
		dec := h.dispatcher.router.Recommend("constraint-check", "high")
		data, _ := json.Marshal(dec)
		text = fmt.Sprintf("*🔍 Routing recommendation:*\n```%s```", string(data))
	}
	return []interface{}{
		slackSection(text),
		map[string]interface{}{
			"type": "actions",
			"elements": []interface{}{
				slackButton("execute_leverage", "execute_leverage", "Execute", "primary"),
				slackButton("override_constraint", "override_constraint", "Override", ""),
			},
		},
	}
}

// buildDispatchBlocks triggers an agent and returns a Block Kit confirmation.
// args may be "<agent>" or "<agent> at #<issue>".
func (h *SlackEventHandler) buildDispatchBlocks(ctx context.Context, args string) []interface{} {
	agent := args
	issueContext := ""
	if idx := strings.Index(args, " at "); idx >= 0 {
		agent = strings.TrimSpace(args[:idx])
		issueContext = " at " + strings.TrimSpace(args[idx+4:])
	}
	if agent == "" {
		return slackTextBlocks(":x: Usage: `dispatch <agent> [at #<issue>]`")
	}

	event := Event{
		Type:    EventManual,
		Source:  "slack",
		Payload: map[string]string{"triggered_by": "slack-command"},
	}
	result, err := h.dispatcher.Dispatch(ctx, event, agent, 1)

	var text string
	switch {
	case err != nil:
		text = fmt.Sprintf(":x: Failed to dispatch `%s`: %s", agent, err)
	case result.Action == "dispatched":
		text = fmt.Sprintf(":rocket: Dispatched `%s`%s. Driver: `%s`.", agent, issueContext, result.Driver)
	case result.Action == "queued":
		text = fmt.Sprintf(":inbox_tray: Queued `%s`%s (position %d).", agent, issueContext, result.QueuePos)
	default:
		text = fmt.Sprintf(":pause_button: `%s` skipped — %s", agent, result.Reason)
	}

	return []interface{}{
		slackSection(text),
		map[string]interface{}{
			"type": "actions",
			"elements": []interface{}{
				slackButton("view_status", "view_status", "View Progress", "primary"),
				slackButton("cancel_dispatch_"+agent, "cancel_dispatch_"+agent, "Cancel", "danger"),
			},
		},
	}
}

// buildPauseBlocks broadcasts a pause directive for the given squad.
func (h *SlackEventHandler) buildPauseBlocks(ctx context.Context, squad string) []interface{} {
	squad = strings.TrimSuffix(strings.TrimSpace(squad), " squad")
	if squad == "" {
		return slackTextBlocks(":x: Usage: `pause <squad>`")
	}
	if err := h.dispatcher.Coord().Broadcast(ctx, "slack-bot", "directive", "pause-squad:"+squad); err != nil {
		return slackTextBlocks(fmt.Sprintf(":x: Could not pause %s squad: %s", squad, err))
	}
	return []interface{}{
		slackSection(fmt.Sprintf(
			":pause_button: *%s squad paused.* Directive broadcast — agents drain on next tick.", squad,
		)),
		map[string]interface{}{
			"type": "actions",
			"elements": []interface{}{
				slackButton("resume_squad_"+squad, "resume_squad_"+squad, "Resume", "primary"),
				slackButton("view_status", "view_status", "View Status", ""),
			},
		},
	}
}

// buildResumeBlocks broadcasts a resume directive for the given squad.
func (h *SlackEventHandler) buildResumeBlocks(ctx context.Context, squad string) []interface{} {
	squad = strings.TrimSuffix(strings.TrimSpace(squad), " squad")
	if squad == "" {
		return slackTextBlocks(":x: Usage: `resume <squad>`")
	}
	if err := h.dispatcher.Coord().Broadcast(ctx, "slack-bot", "directive", "resume-squad:"+squad); err != nil {
		return slackTextBlocks(fmt.Sprintf(":x: Could not resume %s squad: %s", squad, err))
	}
	return slackTextBlocks(fmt.Sprintf(":arrow_forward: *%s squad resumed.* Directive broadcast.", squad))
}

// buildBudgetOverrideBlocks unpauses a budget-exhausted agent and returns a
// Block Kit confirmation. The agent name comes from args.
func (h *SlackEventHandler) buildBudgetOverrideBlocks(ctx context.Context, agent string) []interface{} {
	if agent == "" {
		return slackTextBlocks(":x: Usage: `budget override <agent>`")
	}
	if h.budgetStore == nil {
		return slackTextBlocks(":x: Budget store not available")
	}
	if err := h.budgetStore.Unpause(ctx, agent); err != nil {
		return slackTextBlocks(fmt.Sprintf(":x: Could not override budget for `%s`: %s", agent, err))
	}
	return slackTextBlocks(fmt.Sprintf(":white_check_mark: Budget override applied — `%s` is unpaused and will resume on next dispatch.", agent))
}

// buildHelpBlocks returns a Block Kit help card listing all supported commands.
func (h *SlackEventHandler) buildHelpBlocks() []interface{} {
	text := "*Octi Pulpo Bot Commands*\n\n" +
		"`status` — swarm pass rate, queue depth, sprint summary\n" +
		"`constraint` — identify the #1 system bottleneck\n" +
		"`dispatch <agent>` — trigger an agent run immediately\n" +
		"`dispatch <agent> at #<issue>` — trigger with issue context\n" +
		"`pause <squad>` — broadcast pause directive to squad agents\n" +
		"`resume <squad>` — broadcast resume directive to squad agents\n" +
		"`budget override <agent>` — unpause a budget-exhausted agent\n" +
		"`help` — show this message"
	return slackTextBlocks(text)
}

// postReply sends Block Kit blocks as a Slack reply.
// Uses chat.postMessage (thread reply) when a bot token is set;
// falls back to the incoming webhook otherwise.
func (h *SlackEventHandler) postReply(ctx context.Context, channel, ts string, blocks []interface{}) error {
	if h.botToken != "" {
		return h.postViaAPI(ctx, channel, ts, blocks)
	}
	if h.notifier != nil && h.notifier.Enabled() {
		return h.notifier.post(ctx, map[string]interface{}{"blocks": blocks})
	}
	return nil
}

// postViaAPI posts a threaded reply via the Slack Web API.
func (h *SlackEventHandler) postViaAPI(ctx context.Context, channel, ts string, blocks []interface{}) error {
	payload := map[string]interface{}{
		"channel":   channel,
		"thread_ts": ts,
		"blocks":    blocks,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.botToken)

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack api post: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack api error: %s", result.Error)
	}
	return nil
}

// verifySignature validates the Slack v0 HMAC-SHA256 request signature.
// Rejects requests older than 5 minutes to prevent replay attacks.
// Dev mode: if signingSecret is empty, all requests are accepted.
func (h *SlackEventHandler) verifySignature(r *http.Request, body []byte) bool {
	if h.signingSecret == "" {
		return true // dev mode — no secret configured
	}
	ts := r.Header.Get("X-Slack-Request-Timestamp")
	sig := r.Header.Get("X-Slack-Signature")

	if t, err := strconv.ParseInt(ts, 10, 64); err == nil {
		if time.Since(time.Unix(t, 0)) > 5*time.Minute {
			return false // stale request
		}
	}

	base := "v0:" + ts + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(h.signingSecret))
	mac.Write([]byte(base))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// slackSection builds a Block Kit section block with mrkdwn text.
func slackSection(text string) map[string]interface{} {
	return map[string]interface{}{
		"type": "section",
		"text": map[string]string{"type": "mrkdwn", "text": text},
	}
}

// slackTextBlocks returns a single-section Block Kit message.
func slackTextBlocks(text string) []interface{} {
	return []interface{}{slackSection(text)}
}

