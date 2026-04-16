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

	// repoSeniors maps live repo names to senior/architect agents
	// that should be dispatched on "need-help" signals. Keyed to
	// LiveRepos (see fossil_regression_test.go).
	repoSeniors map[string]string

	// triageAgents maps repo names to triage agents
	// that should be dispatched on "blocked" signals.
	triageAgents map[string]string
}

// NewSignalWatcher creates a signal watcher connected to Redis pub/sub.
func NewSignalWatcher(dispatcher *Dispatcher, rdb *redis.Client, namespace string) *SignalWatcher {
	return &SignalWatcher{
		dispatcher: dispatcher,
		rdb:        rdb,
		namespace:  namespace,
		log:        log.New(os.Stderr, "signal-watcher: ", log.LstdFlags),
		repoSeniors: map[string]string{
			"kernel":     "kernel-sr",
			"shellforge": "shellforge-sr",
			"clawta":     "clawta-sr",
			"sentinel":   "sentinel-sr",
			"llmint":     "llmint-sr",
			"octi":       "octi-sr",
			"workspace":  "workspace-sr",
			"ganglia":    "ganglia-sr",
		},
		triageAgents: map[string]string{
			"kernel": "triage-failing-ci-agent",
		},
	}
}

// signalPayload is the parsed signal from Redis pub/sub.
type signalPayload struct {
	AgentID   string `json:"agent_id"`
	Type      string `json:"type"`    // completed, blocked, need-help, directive
	Payload   string `json:"payload"`
	Repo      string `json:"repo"`    // optional repo context (e.g. "chitinhq/kernel")
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
		// Squad-era director broadcast was excised in octi#271 Phase 1
		// (all 9 *-em agents targeted by allEMs were dead). Directive
		// signals now log for observability only; re-introduce a
		// handler here if a live EM role is ever restored.
		sw.log.Printf("directive signal from %s (ignored; squad fan-out removed in octi#271)", sig.AgentID)
	case "completed":
		// Completion signals are handled by the chain system in the worker,
		// but we log them for observability.
		sw.log.Printf("completion signal from %s (handled by chains)", sig.AgentID)
	default:
		sw.log.Printf("unhandled signal type %q from %s", sig.Type, sig.AgentID)
	}
}

// handleNeedHelp dispatches the repo's senior developer to assist.
func (sw *SignalWatcher) handleNeedHelp(ctx context.Context, sig signalPayload) {
	repo := inferSquad(sig.AgentID)
	senior, ok := sw.repoSeniors[repo]
	if !ok {
		sw.log.Printf("no senior mapped for repo %q (agent: %s)", repo, sig.AgentID)
		return
	}

	event := Event{
		Type:   EventSignal,
		Source: sig.AgentID,
		Repo:   sig.Repo,
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

// handleBlocked dispatches the repo's triage agent.
func (sw *SignalWatcher) handleBlocked(ctx context.Context, sig signalPayload) {
	repo := inferSquad(sig.AgentID)
	triage, ok := sw.triageAgents[repo]
	if !ok {
		sw.log.Printf("no triage agent for repo %q (agent: %s)", repo, sig.AgentID)
		return
	}

	event := Event{
		Type:   EventSignal,
		Source: sig.AgentID,
		Repo:   sig.Repo,
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

// inferSquad extracts the repo name from an agent ID.
// e.g., "kernel-qa" -> "kernel", "octi-sr" -> "octi".
// The function name is retained for call-site compatibility; the term
// "squad" is historical and now synonymous with "repo" after the
// org collapse (see octi#271).
func inferSquad(agentID string) string {
	// Direct prefix match against live repos.
	for _, repo := range LiveRepos {
		if strings.HasPrefix(agentID, repo+"-") {
			return repo
		}
	}

	// Check suffix for agents like "ci-triage-agent-kernel".
	for _, repo := range LiveRepos {
		if strings.HasSuffix(agentID, "-"+repo) {
			return repo
		}
	}

	// Fallback: first segment.
	parts := strings.SplitN(agentID, "-", 2)
	return parts[0]
}
