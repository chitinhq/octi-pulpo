package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/coordination"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/redis/go-redis/v9"
)

// DispatchResult is the outcome of a dispatch decision.
type DispatchResult struct {
	Action    string `json:"action"`     // "dispatched", "skipped", "queued"
	Agent     string `json:"agent"`      // agent name
	Reason    string `json:"reason"`     // human-readable explanation
	Driver    string `json:"driver"`     // chosen driver (empty if skipped)
	QueuePos  int64  `json:"queue_pos"`  // position in queue (0 if not queued)
	ClaimID   string `json:"claim_id"`   // coordination claim ID (empty if skipped)
	Timestamp string `json:"timestamp"`
}

// DispatchRecord is persisted to Redis for observability.
type DispatchRecord struct {
	Agent     string `json:"agent"`
	Event     Event  `json:"event"`
	Result    string `json:"result"` // action taken
	Reason    string `json:"reason"`
	Driver    string `json:"driver"`
	Timestamp string `json:"timestamp"`
}

// Dispatcher coordinates all agent scheduling based on events.
type Dispatcher struct {
	rdb       *redis.Client
	router    *routing.Router
	coord     *coordination.Engine
	events    *EventRouter
	queueFile string // ~/.agentguard/queue.txt (compatibility bridge)
	namespace string
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
// The decision flow:
//  1. Check cooldown -- has this agent been dispatched too recently?
//  2. Check coord_claim -- is another instance already running/claimed?
//  3. Check route_recommend -- is a healthy driver + budget available?
//  4. If yes to all: claim the task + enqueue to priority queue
//  5. If driver exhausted: queue for later (backpressure, don't fail)
//  6. Return the decision with reason
func (d *Dispatcher) Dispatch(ctx context.Context, event Event, agentName string, priority int) (DispatchResult, error) {
	now := time.Now().UTC()
	result := DispatchResult{
		Agent:     agentName,
		Timestamp: now.Format(time.RFC3339),
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

	// 3. Check driver health/budget
	routeDecision := d.router.Recommend(agentName, "high")

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

	// Set cooldown based on event rules
	cooldown := d.events.CooldownFor(agentName)
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
	result.Action = "dispatched"
	result.Reason = fmt.Sprintf("dispatched via %s (tier: %s, confidence: %.1f)", routeDecision.Driver, routeDecision.Tier, routeDecision.Confidence)
	result.Driver = routeDecision.Driver
	result.ClaimID = claim.ClaimID
	result.QueuePos = queueDepth

	d.recordDispatch(ctx, agentName, event, result)
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

func (d *Dispatcher) recordDispatch(ctx context.Context, agentName string, event Event, result DispatchResult) {
	record := DispatchRecord{
		Agent:     agentName,
		Event:     event,
		Result:    result.Action,
		Reason:    result.Reason,
		Driver:    result.Driver,
		Timestamp: result.Timestamp,
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
