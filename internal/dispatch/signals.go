package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
)

// SignalWatcher subscribes to Redis pub/sub for coordination signals
// and dispatches agents based on signal type.
//
// Agents broadcast signals via the coord_signal MCP tool. The watcher
// reacts to those signals and triggers follow-up agents through the dispatcher.
type SignalWatcher struct {
	dispatcher *Dispatcher
	rdb        *redis.Client
	namespace  string
	log        *log.Logger

	// squadSeniors maps squad prefixes to senior/architect agents
	// that should be dispatched on "need-help" signals.
	squadSeniors map[string]string

	// triageAgents maps squad prefixes to triage agents
	// that should be dispatched on "blocked" signals.
	triageAgents map[string]string

	// allEMs is the list of all EM agents that receive director broadcasts.
	allEMs []string
}

// NewSignalWatcher creates a signal watcher connected to Redis pub/sub.
func NewSignalWatcher(dispatcher *Dispatcher, rdb *redis.Client, namespace string) *SignalWatcher {
	return &SignalWatcher{
		dispatcher: dispatcher,
		rdb:        rdb,
		namespace:  namespace,
		log:        log.New(os.Stderr, "signal-watcher: ", log.LstdFlags),
		squadSeniors: map[string]string{
			"kernel":     "kernel-sr",
			"cloud":      "cloud-sr",
			"shellforge": "shellforge-sr",
			"octi-pulpo": "octi-pulpo-sr",
			"studio":     "studio-sr",
			"office-sim": "office-sim-sr",
		},
		triageAgents: map[string]string{
			"kernel": "triage-failing-ci-agent",
			"cloud":  "ci-triage-agent-cloud",
		},
		allEMs: []string{
			"kernel-em", "cloud-em", "shellforge-em",
			"octi-pulpo-em", "studio-em", "marketing-em",
			"design-em", "site-em", "qa-em",
		},
	}
}

// signalPayload is the parsed signal from Redis pub/sub.
type signalPayload struct {
	AgentID   string `json:"agent_id"`
	Type      string `json:"type"`    // completed, blocked, need-help, directive
	Payload   string `json:"payload"`
	Timestamp string `json:"timestamp"`
}

// Watch subscribes to the coordination signal channel and dispatches
// agents in response to signals. Blocks until context is cancelled.
func (sw *SignalWatcher) Watch(ctx context.Context) error {
	channel := sw.namespace + ":signal-stream"
	pubsub := sw.rdb.Subscribe(ctx, channel)
	defer pubsub.Close()

	// Wait for subscription confirmation
	_, err := pubsub.Receive(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to %s: %w", channel, err)
	}

	sw.log.Printf("subscribed to %s", channel)

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil // channel closed
			}
			sw.handleSignal(ctx, msg.Payload)
		}
	}
}

// handleSignal parses a signal message and dispatches appropriate agents.
func (sw *SignalWatcher) handleSignal(ctx context.Context, raw string) {
	var sig signalPayload
	if err := json.Unmarshal([]byte(raw), &sig); err != nil {
		sw.log.Printf("parse error: %v", err)
		return
	}

	sw.log.Printf("received %s from %s: %s", sig.Type, sig.AgentID, sig.Payload)

	switch sig.Type {
	case "need-help":
		sw.handleNeedHelp(ctx, sig)
	case "blocked":
		sw.handleBlocked(ctx, sig)
	case "directive":
		sw.handleDirective(ctx, sig)
	case "completed":
		// Completion signals are handled by the chain system in the worker,
		// but we log them for observability.
		sw.log.Printf("completion signal from %s (handled by chains)", sig.AgentID)
	default:
		sw.log.Printf("unhandled signal type %q from %s", sig.Type, sig.AgentID)
	}
}

// handleNeedHelp dispatches the squad's senior developer to assist.
func (sw *SignalWatcher) handleNeedHelp(ctx context.Context, sig signalPayload) {
	squad := inferSquad(sig.AgentID)
	senior, ok := sw.squadSeniors[squad]
	if !ok {
		sw.log.Printf("no senior mapped for squad %q (agent: %s)", squad, sig.AgentID)
		return
	}

	event := Event{
		Type:   EventSignal,
		Source: sig.AgentID,
		Payload: map[string]string{
			"signal_type":  "need-help",
			"from_agent":   sig.AgentID,
			"help_context": sig.Payload,
		},
		Priority: 1,
	}

	result, err := sw.dispatcher.Dispatch(ctx, event, senior, 1)
	if err != nil {
		sw.log.Printf("dispatch %s for need-help: %v", senior, err)
		return
	}
	sw.log.Printf("need-help -> dispatched %s (%s)", senior, result.Action)
}

// handleBlocked dispatches the squad's triage agent.
func (sw *SignalWatcher) handleBlocked(ctx context.Context, sig signalPayload) {
	squad := inferSquad(sig.AgentID)
	triage, ok := sw.triageAgents[squad]
	if !ok {
		sw.log.Printf("no triage agent for squad %q (agent: %s)", squad, sig.AgentID)
		return
	}

	event := Event{
		Type:   EventSignal,
		Source: sig.AgentID,
		Payload: map[string]string{
			"signal_type":    "blocked",
			"from_agent":     sig.AgentID,
			"blocker_detail": sig.Payload,
		},
		Priority: 1,
	}

	result, err := sw.dispatcher.Dispatch(ctx, event, triage, 1)
	if err != nil {
		sw.log.Printf("dispatch %s for blocked: %v", triage, err)
		return
	}
	sw.log.Printf("blocked -> dispatched %s (%s)", triage, result.Action)
}

// handleDirective broadcasts to all EM agents when a directive is published.
func (sw *SignalWatcher) handleDirective(ctx context.Context, sig signalPayload) {
	event := Event{
		Type:   EventSignal,
		Source: sig.AgentID,
		Payload: map[string]string{
			"signal_type": "directive",
			"from_agent":  sig.AgentID,
			"directive":   sig.Payload,
		},
		Priority: 1,
	}

	var dispatched int
	for _, em := range sw.allEMs {
		result, err := sw.dispatcher.Dispatch(ctx, event, em, 1)
		if err != nil {
			sw.log.Printf("dispatch %s for directive: %v", em, err)
			continue
		}
		if result.Action == "dispatched" {
			dispatched++
		}
	}
	sw.log.Printf("directive -> dispatched %d/%d EMs", dispatched, len(sw.allEMs))
}

// inferSquad extracts the squad name from an agent ID.
// e.g., "kernel-qa" -> "kernel", "cloud-sr" -> "cloud",
// "ci-triage-agent-cloud" -> "cloud"
func inferSquad(agentID string) string {
	// Direct prefix match for standard naming
	knownSquads := []string{
		"kernel", "cloud", "shellforge", "octi-pulpo",
		"studio", "office-sim", "marketing", "design", "site", "qa",
	}
	for _, squad := range knownSquads {
		if strings.HasPrefix(agentID, squad+"-") {
			return squad
		}
	}

	// Check suffix for agents like "ci-triage-agent-cloud"
	for _, squad := range knownSquads {
		if strings.HasSuffix(agentID, "-"+squad) {
			return squad
		}
	}

	// Fallback: first segment
	parts := strings.SplitN(agentID, "-", 2)
	return parts[0]
}
