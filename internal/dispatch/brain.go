package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// Brain runs a periodic evaluation loop that decides what to dispatch
// based on system state. It supplements the timer (which ensures baseline
// scheduling) with intelligence:
//
//   - Backpressure recovery: when drivers recover, dequeue waiting agents
//   - Chain monitoring: detect stalled chains (e.g., QA dispatched but never ran)
//   - Queue health: alert on growing queue depth
//
// The brain runs every tickInterval (default 60s) and takes action as needed.
type Brain struct {
	dispatcher   *Dispatcher
	chains       ChainConfig
	tickInterval time.Duration
	log          *log.Logger
}

// NewBrain creates a dispatch brain.
func NewBrain(dispatcher *Dispatcher, chains ChainConfig) *Brain {
	return &Brain{
		dispatcher:   dispatcher,
		chains:       chains,
		tickInterval: 60 * time.Second,
		log:          log.New(os.Stderr, "brain: ", log.LstdFlags),
	}
}

// Run starts the brain evaluation loop. Blocks until context is cancelled.
func (b *Brain) Run(ctx context.Context) error {
	b.log.Printf("starting brain loop (tick=%s)", b.tickInterval)

	// Fire immediately on start
	b.Tick(ctx)

	ticker := time.NewTicker(b.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			b.log.Printf("brain loop stopped")
			return ctx.Err()
		case <-ticker.C:
			b.Tick(ctx)
		}
	}
}

// Tick runs a single evaluation cycle.
func (b *Brain) Tick(ctx context.Context) {
	// 1. Check backpressure recovery
	b.checkBackpressureRecovery(ctx)

	// 2. Check queue health
	b.checkQueueHealth(ctx)

	// 3. Check for stalled dispatches
	b.checkStalledDispatches(ctx)
}

// checkBackpressureRecovery looks for agents that were queued due to
// driver exhaustion. If drivers have recovered, re-dispatch them.
func (b *Brain) checkBackpressureRecovery(ctx context.Context) {
	depth, err := b.dispatcher.PendingCount(ctx)
	if err != nil || depth == 0 {
		return
	}

	// Check if drivers are healthy now by attempting a route recommendation
	decision := b.dispatcher.router.Recommend("brain-check", "high")
	if decision.Skip {
		// Drivers still exhausted, nothing to do
		return
	}

	b.log.Printf("drivers recovered, %d agents in queue — processing backlog", depth)

	// Don't drain the entire queue in one tick — process up to 5
	maxDrain := 5
	if int(depth) < maxDrain {
		maxDrain = int(depth)
	}

	for i := 0; i < maxDrain; i++ {
		agent, err := b.dispatcher.Dequeue(ctx)
		if err != nil || agent == "" {
			break
		}

		// Re-dispatch through the normal flow (with all checks)
		event := Event{
			Type:   EventType("brain.recovery"),
			Source: "brain",
			Payload: map[string]string{
				"reason": "backpressure_recovery",
			},
			Priority: 2,
		}

		result, err := b.dispatcher.Dispatch(ctx, event, agent, 2)
		if err != nil {
			b.log.Printf("re-dispatch %s: %v", agent, err)
			continue
		}
		b.log.Printf("recovered %s -> %s", agent, result.Action)
	}
}

// checkQueueHealth logs warnings when queue depth is growing.
func (b *Brain) checkQueueHealth(ctx context.Context) {
	depth, err := b.dispatcher.PendingCount(ctx)
	if err != nil {
		return
	}

	if depth > 50 {
		b.log.Printf("WARNING: queue depth %d — possible backpressure or stuck workers", depth)
	} else if depth > 20 {
		b.log.Printf("queue depth elevated: %d", depth)
	}
}

// checkStalledDispatches looks at recent dispatches and warns about
// agents that were dispatched long ago but might be stalled.
func (b *Brain) checkStalledDispatches(ctx context.Context) {
	rdb := b.dispatcher.RedisClient()
	ns := b.dispatcher.Namespace()

	// Check worker results for recent failures
	raw, err := rdb.LRange(ctx, ns+":worker-results", 0, 9).Result()
	if err != nil || len(raw) == 0 {
		return
	}

	var recentFailures int
	for _, r := range raw {
		var result struct {
			Agent    string  `json:"agent"`
			ExitCode int     `json:"exit_code"`
			Duration float64 `json:"duration_sec"`
		}
		if err := json.Unmarshal([]byte(r), &result); err != nil {
			continue
		}
		if result.ExitCode != 0 {
			recentFailures++
		}
	}

	if recentFailures > 5 {
		b.log.Printf("WARNING: %d/%d recent worker results are failures — possible systemic issue", recentFailures, len(raw))
	}
}

// Stats returns current brain-observable metrics for the dispatch status endpoint.
func (b *Brain) Stats(ctx context.Context) map[string]interface{} {
	depth, _ := b.dispatcher.PendingCount(ctx)
	agents, _ := b.dispatcher.PendingAgents(ctx)

	rdb := b.dispatcher.RedisClient()
	ns := b.dispatcher.Namespace()
	okCount, _ := rdb.Get(ctx, ns+":worker-ok").Result()
	failCount, _ := rdb.Get(ctx, ns+":worker-fail").Result()

	return map[string]interface{}{
		"queue_depth":    depth,
		"pending_agents": agents,
		"worker_ok":      okCount,
		"worker_fail":    failCount,
		"chain_count":    len(b.chains),
		"tick_interval":  b.tickInterval.String(),
	}
}

// SetTickInterval overrides the default tick interval (for testing).
func (b *Brain) SetTickInterval(d time.Duration) {
	b.tickInterval = d
}

// FormatChainGraph returns a human-readable representation of the chain config
// for debugging and observability.
func FormatChainGraph(chains ChainConfig) string {
	var out string
	for agent, action := range chains {
		if len(action.OnSuccess) > 0 {
			out += fmt.Sprintf("  %s --success--> %v\n", agent, action.OnSuccess)
		}
		if len(action.OnFailure) > 0 {
			out += fmt.Sprintf("  %s --failure--> %v\n", agent, action.OnFailure)
		}
		if len(action.OnCommit) > 0 {
			out += fmt.Sprintf("  %s --commit---> %v\n", agent, action.OnCommit)
		}
	}
	return out
}
