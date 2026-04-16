package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/budget"
	"github.com/chitinhq/octi-pulpo/internal/coordination"
	"github.com/chitinhq/octi-pulpo/internal/dispatch/swarmcircuit"
	"github.com/chitinhq/octi-pulpo/internal/flow"
	"github.com/chitinhq/octi-pulpo/internal/presence"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/redis/go-redis/v9"
)

// DispatchResult is the outcome of a dispatch decision.
type DispatchResult struct {
	Action     string `json:"action"`      // "dispatched", "skipped", "queued"
	Agent      string `json:"agent"`       // agent name
	Reason     string `json:"reason"`      // human-readable explanation
	Driver     string `json:"driver"`      // chosen driver (empty if skipped)
	Budget     string `json:"budget"`      // effective budget level: "low", "medium", "high"
	QueuePos   int64  `json:"queue_pos"`   // position in queue (0 if not queued)
	ClaimID    string `json:"claim_id"`    // coordination claim ID (empty if skipped)
	DispatchID string `json:"dispatch_id"` // correlation id for cross-sink reconcile (octi#257)
	Timestamp  string `json:"timestamp"`
}

// newDispatchID mints a 16-byte hex correlation id that is threaded through
// DispatchRecord (Redis) and repository_dispatch.client_payload.dispatch_id
// (GitHub Actions) so Sentinel's DetectDispatchOrphans pass (sentinel#70) can
// join the three truth sinks. Deliberately crypto/rand over a new ulid/uuid
// dep — 128 bits of entropy, zero supply chain surface. See octi#257.
func newDispatchID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read on linux is infallible in practice; fall back to a
		// time-based id so a dispatch is never un-joinable.
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// DispatchRecord is persisted to Redis for observability.
type DispatchRecord struct {
	Agent      string `json:"agent"`
	Event      Event  `json:"event"`
	Result     string `json:"result"` // action taken
	Reason     string `json:"reason"`
	Driver     string `json:"driver"`
	Tier       string `json:"tier,omitempty"`        // Ladder Forge tier: local|actions|cloud|desktop|human|unknown
	DispatchID string `json:"dispatch_id,omitempty"` // correlation id joining Redis / gh runs / Neon (octi#257)
	Timestamp  string `json:"timestamp"`
}

// ClassifyTier maps a driver (and event context) to a Ladder Forge tier.
// v0 rules:
//   - gh-actions          -> actions
//   - clawta, openclaw    -> local
//   - anthropic, remote-* -> cloud
//   - needs-human relabel -> human
//   - unknown/blank       -> unknown (T1 local and T4 desktop will report 0 until online)
func ClassifyTier(driver string, event Event) string {
	// Human escalation detected via label on issue.labeled events.
	if event.Type == EventIssueLabeled {
		if lbl := event.Payload["label"]; lbl == "needs-human" || lbl == "agent:blocked" {
			return "human"
		}
	}
	switch driver {
	case "gh-actions", "ghactions", "github-actions":
		return "actions"
	case "copilot-agent":
		// T2b: Enterprise Copilot coding agent (org-level webhook).
		return "copilot"
	case "clawta", "openclaw", "claude-code", "copilot-cli":
		return "local"
	case "anthropic", "claude", "remote", "remote-trigger":
		return "cloud"
	case "desktop", "claude-desktop":
		return "desktop"
	case "":
		return "unknown"
	}
	return "unknown"
}

// Dispatcher coordinates all agent scheduling based on events.
type Dispatcher struct {
	rdb       *redis.Client
	router    *routing.Router
	coord     *coordination.Engine
	events    *EventRouter
	profiles  *ProfileStore         // adaptive cooldowns (nil = use static)
	budget    *budget.BudgetStore   // per-agent budget check (nil = skip)
	presence  *presence.Store       // user presence for concurrency limits (nil = no limit)
	presUser  string                // user ID for presence checks
	queueFile string                // ~/.chitin/queue.txt (compatibility bridge)
	namespace string
	adapters  []Adapter // execution-surface adapters; picked by Name() against routed driver

	// swarmCircuit reflects the swarm-wide pause flag driven by sentinel's
	// circuit-breaker patrol (chitinhq/sentinel internal/circuit). When
	// non-nil and Paused()=true, Dispatch short-circuits with action="paused".
	// Distinct from the per-driver health circuit in routing.Router.
	swarmCircuit *swarmcircuit.Subscriber
}

// SetSwarmCircuit installs the swarm-circuit subscriber on the dispatcher.
// nil disables the gate.
func (d *Dispatcher) SetSwarmCircuit(s *swarmcircuit.Subscriber) { d.swarmCircuit = s }

// SwarmCircuit returns the installed subscriber (may be nil). Surfaced
// so the MCP layer can read snapshot state into dispatch_status.
func (d *Dispatcher) SwarmCircuit() *swarmcircuit.Subscriber { return d.swarmCircuit }

// SetAdapters registers adapters that execute a dispatched task on a real
// surface (HTTP repository_dispatch, Anthropic API, Claude Code CLI, etc.).
// After route selection, Dispatch() invokes the adapter whose Name() matches
// the routed driver. This is what makes result.Action="dispatched" mean "an
// execution surface was actually called" rather than "we enqueued to Redis
// and hoped." See workspace#408 (silent-loss regression) and chitinhq/octi#252
// (replay of dropped PR #245 hunk).
func (d *Dispatcher) SetAdapters(adapters ...Adapter) { d.adapters = adapters }

// selectAdapter returns the registered adapter whose Name() matches driver,
// or nil if none is registered. Gate between routing ("we picked claude-code")
// and execution ("we actually called it").
func (d *Dispatcher) selectAdapter(driver string) Adapter {
	for _, a := range d.adapters {
		if a != nil && a.Name() == driver {
			return a
		}
	}
	return nil
}

// NewDispatcher creates an event-driven dispatcher.
func NewDispatcher(rdb *redis.Client, router *routing.Router, coord *coordination.Engine, events *EventRouter, queueFile, namespace string) *Dispatcher {
	return &Dispatcher{
		rdb:       rdb,
		router:    router,
		coord:     coord,
		events:    events,
		queueFile: queueFile,
		namespace: namespace,
	}
}

// Dispatch decides whether to run an agent based on event + coordination state.
// It uses the dynamic budget level from the current swarm health — preventing
// automatic escalation to expensive API-tier drivers when CLI-tier capacity exists.
// The decision flow:
//  1. Check cooldown -- has this agent been dispatched too recently?
//  2. Check coord_claim -- is another instance already running/claimed?
//  3. Check route_recommend -- is a healthy driver available within budget?
//  4. If yes to all: claim the task + enqueue to priority queue
//  5. If driver exhausted: queue for later (backpressure, don't fail)
//  6. Return the decision with reason
func (d *Dispatcher) Dispatch(ctx context.Context, event Event, agentName string, priority int) (DispatchResult, error) {
	return d.DispatchBudget(ctx, event, agentName, priority, d.router.DynamicBudget())
}

// DispatchBudget is like Dispatch but accepts an explicit budget level ("low", "medium", "high").
// Use this when you need to override the automatic dynamic budget — e.g. for API-tier burst
// capacity via a manual MCP trigger, or in tests that need deterministic routing.
func (d *Dispatcher) DispatchBudget(ctx context.Context, event Event, agentName string, priority int, budget string) (retResult DispatchResult, retErr error) {
	defer flow.Span("swarm.dispatch", map[string]interface{}{
		"agent": agentName, "event_type": event.Type, "priority": priority, "budget": budget,
	})(&retErr)

	now := time.Now().UTC()
	result := DispatchResult{
		Agent:      agentName,
		Budget:     budget,
		DispatchID: newDispatchID(),
		Timestamp:  now.Format(time.RFC3339),
	}

	// 0a. Swarm-circuit gate: if sentinel's patrol has tripped any of the
	// four signals (retry_storm / resource_burn / repo_health /
	// telemetry_integrity), pause new dispatches until an operator
	// resets the breaker. This is fleet-wide, distinct from per-driver
	// health in routing.Router.
	if d.swarmCircuit != nil && d.swarmCircuit.Paused() {
		st := d.swarmCircuit.Snapshot()
		result.Action = "paused"
		result.Reason = fmt.Sprintf("swarm circuit open (%s): %s", st.Signal, st.Reason)
		d.recordDispatch(ctx, agentName, event, result)
		return result, nil
	}

	// 0. Validate: repo-scoped events must carry a non-empty Repo.
	// Adapter CanAccept() rejects empty-repo dispatches, so such events
	// evaporate silently at the adapter layer — the silent-loss bug from
	// workspace#408. Reject loudly here with telemetry instead so the
	// producer can be located via recent-dispatches. System-wide events
	// (timer, manual, signal, slack.action, budget.change, completion,
	// brain.recovery) are exempt — they legitimately have no repo.
	if event.RequiresRepo() && event.Repo == "" {
		result.Action = "skipped"
		result.Reason = fmt.Sprintf("event.Repo empty for repo-scoped type %q (producer bug)", event.Type)
		d.recordDispatch(ctx, agentName, event, result)
		return result, fmt.Errorf("dispatch: event.Repo required for event type %q (agent=%s source=%s)", event.Type, agentName, event.Source)
	}

	// 1. Check cooldown
	cooldownKey := d.key("cooldown:" + agentName)
	exists, err := d.rdb.Exists(ctx, cooldownKey).Result()
	if err != nil {
		return result, fmt.Errorf("check cooldown: %w", err)
	}
	if exists > 0 {
		ttl, _ := d.rdb.TTL(ctx, cooldownKey).Result()
		result.Action = "skipped"
		result.Reason = fmt.Sprintf("cooldown active (%s remaining)", ttl.Round(time.Second))
		d.recordDispatch(ctx, agentName, event, result)
		return result, nil
	}

	// 2. Check coordination claims -- is this agent already running?
	claimKey := d.key("claim:" + agentName)
	claimExists, err := d.rdb.Exists(ctx, claimKey).Result()
	if err != nil {
		return result, fmt.Errorf("check claim: %w", err)
	}
	if claimExists > 0 {
		result.Action = "skipped"
		result.Reason = "agent already has active claim (another instance running)"
		d.recordDispatch(ctx, agentName, event, result)
		return result, nil
	}

	// 2.5 Check per-agent budget (if budget store is configured)
	if d.budget != nil {
		allowed, budgetErr := d.budget.CheckAndIncrement(ctx, agentName, 10, priorityStr(priority))
		if budgetErr != nil {
			// Budget check failed — fail-open (don't block on budget errors)
			_ = budgetErr
		} else if !allowed {
			result.Action = "skipped"
			result.Reason = "budget exhausted or below priority threshold"
			d.recordDispatch(ctx, agentName, event, result)
			return result, nil
		}
	}

	// 2.7 Presence-based concurrency: if user is focused, limit concurrent claims to 2.
	// On error, log and proceed (fail-open) rather than silently disabling throttling.
	if d.presence != nil && d.presUser != "" {
		state, presErr := d.presence.Get(ctx, d.presUser)
		if presErr != nil {
			fmt.Fprintf(os.Stderr, "dispatch: presence check failed: %v (proceeding without throttle)\n", presErr)
		} else if state == presence.Focused {
			claims, claimErr := d.coord.ActiveClaims(ctx)
			if claimErr != nil {
				fmt.Fprintf(os.Stderr, "dispatch: active claims check failed: %v (proceeding without throttle)\n", claimErr)
			} else if len(claims) >= 2 {
				result.Action = "skipped"
				result.Reason = fmt.Sprintf("user focused — %d active claims (max 2)", len(claims))
				d.recordDispatch(ctx, agentName, event, result)
				return result, nil
			}
		}
	}

	// 3. Check driver health/budget — budget level determined by caller (dynamic or explicit)
	routeDecision := d.router.Recommend(agentName, budget)

	if routeDecision.Skip {
		// All drivers exhausted -- queue for later (backpressure)
		if err := d.Enqueue(ctx, agentName, priority); err != nil {
			return result, fmt.Errorf("enqueue for backpressure: %w", err)
		}
		queueDepth, _ := d.PendingCount(ctx)
		result.Action = "queued"
		result.Reason = fmt.Sprintf("all drivers exhausted — queued for retry (depth: %d)", queueDepth)
		result.QueuePos = queueDepth
		d.recordDispatch(ctx, agentName, event, result)
		return result, nil
	}

	// 4. All checks pass -- claim + enqueue + set cooldown
	claim, err := d.coord.ClaimTask(ctx, agentName, fmt.Sprintf("event:%s", event.Type), 900)
	if err != nil {
		return result, fmt.Errorf("claim task: %w", err)
	}

	// Set cooldown — use adaptive cooldown if profiles are available, else static
	var cooldown time.Duration
	if d.profiles != nil {
		cooldown = d.profiles.AdaptiveCooldown(ctx, agentName)
	} else {
		cooldown = d.events.CooldownFor(agentName)
	}
	if cooldown > 0 {
		d.rdb.Set(ctx, cooldownKey, "1", cooldown)
	}

	// Enqueue to priority queue
	if err := d.Enqueue(ctx, agentName, priority); err != nil {
		return result, fmt.Errorf("enqueue: %w", err)
	}

	// Bridge to file queue for backward compatibility
	if bridgeErr := d.BridgeToFileQueue(agentName); bridgeErr != nil {
		// Non-fatal: Redis queue is the source of truth, file queue is just compatibility
		_ = bridgeErr
	}

	queueDepth, _ := d.PendingCount(ctx)
	result.Driver = routeDecision.Driver
	result.ClaimID = claim.ClaimID
	result.QueuePos = queueDepth

	// 5. Actually call the adapter for the routed driver. Until this returns
	// successfully, we have only *routed* — we have not *dispatched*. Action
	// "dispatched" must mean an execution surface was invoked and accepted
	// the task (workspace#408 / chitinhq/octi#252: silent-loss fix replay).
	adapter := d.selectAdapter(routeDecision.Driver)
	if adapter == nil {
		if len(d.adapters) == 0 {
			// Legacy path: no adapters registered at all. Preserve the old
			// queue-only behavior so callers that consume from the Redis
			// queue directly don't regress. Reason string marks it as
			// queue-only so observers can distinguish it from HTTP-confirmed
			// dispatch.
			result.Action = "dispatched"
			result.Reason = fmt.Sprintf("queued via %s (tier: %s, confidence: %.1f; no adapter registered)", routeDecision.Driver, routeDecision.Tier, routeDecision.Confidence)
			d.recordDispatch(ctx, agentName, event, result)
			if d.profiles != nil {
				_ = d.profiles.RecordDispatch(ctx, agentName)
			}
			return result, nil
		}
		// Adapters exist but none matches the routed driver: don't claim
		// success with no execution surface attached.
		result.Action = "unroutable"
		result.Reason = fmt.Sprintf("no adapter registered for driver %q", routeDecision.Driver)
		d.recordDispatch(ctx, agentName, event, result)
		return result, nil
	}

	task := &Task{
		ID:         fmt.Sprintf("%s-%d", agentName, now.UnixNano()),
		Type:       string(event.Type),
		Repo:       event.Repo,
		Priority:   priorityStr(priority),
		DispatchID: result.DispatchID,
	}

	adapterResult, adapterErr := adapter.Dispatch(ctx, task)
	if adapterErr != nil {
		result.Action = "failed"
		result.Reason = fmt.Sprintf("adapter %s dispatch failed: %v", adapter.Name(), adapterErr)
		d.recordDispatch(ctx, agentName, event, result)
		return result, nil
	}
	if adapterResult != nil && adapterResult.Status == "failed" {
		errMsg := adapterResult.Error
		if errMsg == "" {
			errMsg = "adapter reported failed status"
		}
		result.Action = "failed"
		result.Reason = fmt.Sprintf("adapter %s: %s", adapter.Name(), errMsg)
		d.recordDispatch(ctx, agentName, event, result)
		return result, nil
	}

	result.Action = "dispatched"
	result.Reason = fmt.Sprintf("dispatched via %s (tier: %s, confidence: %.1f)", routeDecision.Driver, routeDecision.Tier, routeDecision.Confidence)

	d.recordDispatch(ctx, agentName, event, result)

	// Wire agent_leaderboard sink: increment dispatches_total counter so the
	// MCP leaderboard tool has something to report even when the completion
	// callback (RecordWorkerResult) never fires — which is the norm for
	// GH-Actions and Anthropic-API drivers. See workspace#408.
	if d.profiles != nil {
		_ = d.profiles.RecordDispatch(ctx, agentName)
	}
	return result, nil
}

// DispatchEvent routes an event through the event rules and dispatches matching agents.
func (d *Dispatcher) DispatchEvent(ctx context.Context, event Event) ([]DispatchResult, error) {
	matches := d.events.Match(event)
	if len(matches) == 0 {
		return nil, nil
	}

	var results []DispatchResult
	for _, rule := range matches {
		priority := rule.Priority
		if event.Priority > 0 && event.Priority < priority {
			priority = event.Priority // event can escalate, not downgrade
		}
		result, err := d.Dispatch(ctx, event, rule.AgentName, priority)
		if err != nil {
			results = append(results, DispatchResult{
				Agent:     rule.AgentName,
				Action:    "error",
				Reason:    err.Error(),
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

// Enqueue adds agent to the priority queue (Redis sorted set).
// Score = priority * 1e12 + unix_ms (lower score = higher priority, FIFO within tier).
func (d *Dispatcher) Enqueue(ctx context.Context, agentName string, priority int) error {
	score := float64(priority)*1e12 + float64(time.Now().UnixMilli())
	return d.rdb.ZAdd(ctx, d.key("dispatch-queue"), redis.Z{
		Score:  score,
		Member: agentName,
	}).Err()
}

// Dequeue returns the highest-priority agent (lowest score) and removes it from the queue.
func (d *Dispatcher) Dequeue(ctx context.Context) (string, error) {
	results, err := d.rdb.ZPopMin(ctx, d.key("dispatch-queue"), 1).Result()
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "", nil
	}
	return results[0].Member.(string), nil
}

// PendingCount returns the queue depth.
func (d *Dispatcher) PendingCount(ctx context.Context) (int64, error) {
	return d.rdb.ZCard(ctx, d.key("dispatch-queue")).Result()
}

// PendingAgents returns all agents currently in the queue, ordered by priority.
func (d *Dispatcher) PendingAgents(ctx context.Context) ([]string, error) {
	return d.rdb.ZRange(ctx, d.key("dispatch-queue"), 0, -1).Result()
}

// RecentDispatches returns the last N dispatch decisions for observability.
func (d *Dispatcher) RecentDispatches(ctx context.Context, limit int) ([]DispatchRecord, error) {
	raw, err := d.rdb.LRange(ctx, d.key("dispatch-log"), 0, int64(limit)-1).Result()
	if err != nil {
		return nil, err
	}
	var records []DispatchRecord
	for _, r := range raw {
		var rec DispatchRecord
		if err := json.Unmarshal([]byte(r), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// SetCooldown manually sets a cooldown for an agent.
func (d *Dispatcher) SetCooldown(ctx context.Context, agentName string, duration time.Duration) error {
	return d.rdb.Set(ctx, d.key("cooldown:"+agentName), "1", duration).Err()
}

// ClearCooldown removes a cooldown for an agent.
func (d *Dispatcher) ClearCooldown(ctx context.Context, agentName string) error {
	return d.rdb.Del(ctx, d.key("cooldown:"+agentName)).Err()
}

// ReleaseClaim releases the coordination claim for an agent.
func (d *Dispatcher) ReleaseClaim(ctx context.Context, agentName string) error {
	return d.coord.ReleaseClaim(ctx, agentName)
}

// RecordWorkerResult records a worker execution result for observability.
// If profiles are enabled, also records to the agent profile for adaptive cooldowns.
func (d *Dispatcher) RecordWorkerResult(ctx context.Context, agentName string, exitCode int, durationSec float64, hadCommits bool) {
	now := time.Now().UTC()
	result := map[string]interface{}{
		"agent":        agentName,
		"exit_code":    exitCode,
		"duration_sec": durationSec,
		"had_commits":  hadCommits,
		"timestamp":    now.Format(time.RFC3339),
	}
	data, err := json.Marshal(result)
	if err != nil {
		return
	}

	pipe := d.rdb.Pipeline()
	pipe.LPush(ctx, d.key("worker-results"), data)
	pipe.LTrim(ctx, d.key("worker-results"), 0, 999) // keep last 1000
	if exitCode == 0 {
		pipe.Incr(ctx, d.key("worker-ok"))
	} else {
		pipe.Incr(ctx, d.key("worker-fail"))
	}
	pipe.Exec(ctx)

	// Record to profile store for adaptive cooldowns
	if d.profiles != nil {
		d.profiles.RecordRun(ctx, agentName, RunResult{
			ExitCode:   exitCode,
			Duration:   durationSec,
			HadCommits: hadCommits,
			Timestamp:  now.Format(time.RFC3339),
		})
		// Leaderboard sink: on a real successful run (exit=0 + commits), bump the
		// successes_total counter + timestamp so agent_leaderboard can surface it.
		if exitCode == 0 && hadCommits {
			_ = d.profiles.RecordSuccess(ctx, agentName, "")
		}
	}
}

// SetProfiles enables adaptive cooldowns on the dispatcher.
func (d *Dispatcher) SetProfiles(ps *ProfileStore) {
	d.profiles = ps
}

// SetBudget enables per-agent budget checking in the dispatch pipeline.
func (d *Dispatcher) SetBudget(b *budget.BudgetStore) {
	d.budget = b
}

// SetPresence enables presence-based concurrency limiting. When the user is
// focused, dispatches are throttled to at most 2 concurrent claims.
func (d *Dispatcher) SetPresence(ps *presence.Store, user string) {
	d.presence = ps
	d.presUser = user
}

// RedisClient returns the underlying Redis client (for workers that need direct queue access).
func (d *Dispatcher) RedisClient() *redis.Client {
	return d.rdb
}

// Namespace returns the dispatcher's namespace prefix.
func (d *Dispatcher) Namespace() string {
	return d.namespace
}

// Coord returns the coordination engine.
func (d *Dispatcher) Coord() *coordination.Engine {
	return d.coord
}

func (d *Dispatcher) recordDispatch(ctx context.Context, agentName string, event Event, result DispatchResult) {
	// Defense-in-depth: every record persisted must carry a dispatch_id so
	// Sentinel's DetectDispatchOrphans (sentinel#70) join key is never empty,
	// even if a caller constructed DispatchResult outside DispatchBudget.
	// See octi#257.
	if result.DispatchID == "" {
		result.DispatchID = newDispatchID()
	}
	record := DispatchRecord{
		Agent:      agentName,
		Event:      event,
		Result:     result.Action,
		Reason:     result.Reason,
		Driver:     result.Driver,
		Tier:       ClassifyTier(result.Driver, event),
		DispatchID: result.DispatchID,
		Timestamp:  result.Timestamp,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}

	pipe := d.rdb.Pipeline()
	pipe.LPush(ctx, d.key("dispatch-log"), data)
	pipe.LTrim(ctx, d.key("dispatch-log"), 0, 499) // keep last 500
	pipe.Exec(ctx)
}

func (d *Dispatcher) key(suffix string) string {
	return d.namespace + ":" + suffix
}

// priorityStr converts a numeric priority to the string budget priorities use.
func priorityStr(priority int) string {
	switch {
	case priority == 0:
		return "CRITICAL"
	case priority == 1:
		return "HIGH"
	case priority <= 2:
		return "NORMAL"
	default:
		return "BACKGROUND"
	}
}
