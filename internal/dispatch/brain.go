package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
)

// Constraint represents the single most important bottleneck in the system.
type Constraint struct {
	Type        string // "all_drivers_down", "p0_bugs", "idle_agents", "stale_prs", "stale_approved_prs", "none"
	Description string
	Severity    int // 0=critical, 1=high, 2=normal
}

// LeverageAction is the highest-value action the brain can take right now.
type LeverageAction struct {
	Agent    string
	IssueNum int
	Repo     string
	Score    float64
	Reason   string
}

// Brain runs a periodic evaluation loop that decides what to dispatch
// based on system state. It supplements the timer (which ensures baseline
// scheduling) with intelligence:
//
//   - Backpressure recovery: when drivers recover, dequeue waiting agents
//   - Chain monitoring: detect stalled chains (e.g., QA dispatched but never ran)
//   - Queue health: alert on growing queue depth
//   - Constraint analysis: identify the ONE bottleneck and focus on it
//   - Sprint store sync: periodically refresh issue data from GitHub
//   - Driver health probe: log stale-OPEN drivers every 15 min
type Brain struct {
	dispatcher    *Dispatcher
	chains        ChainConfig
	tickInterval  time.Duration
	log           *log.Logger
	sprintStore   *sprint.Store
	profiles      *ProfileStore
	lastSync      time.Time
	lastProbeAt   time.Time
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

// SetSprintStore enables sprint-aware dispatch in the brain.
func (b *Brain) SetSprintStore(s *sprint.Store) {
	b.sprintStore = s
}

// SetProfileStore enables adaptive-cooldown-aware constraint analysis.
func (b *Brain) SetProfileStore(ps *ProfileStore) {
	b.profiles = ps
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
	// 1. Sync sprint store every 5 minutes (rate limit friendly)
	b.maybeSyncSprint(ctx)

	// 2. Legacy checks
	b.checkBackpressureRecovery(ctx)
	b.checkQueueHealth(ctx)
	b.checkStalledDispatches(ctx)

	// 3. Periodic driver health probe (every 15 min)
	b.maybeProbeDriverHealth()

	// 4. Constraint-driven dispatch (if sprint store is available)
	if b.sprintStore != nil {
		constraint := b.identifyConstraint(ctx)
		if constraint.Type != "none" && constraint.Type != "all_drivers_down" {
			action := b.highestLeverageAction(ctx, constraint)
			if action != nil {
				b.executeLeverageAction(ctx, *action)
			}
		}
		if constraint.Type != "none" {
			b.log.Printf("constraint: [%s] %s (severity=%d)", constraint.Type, constraint.Description, constraint.Severity)
		}
	}
}

// maybeSyncSprint syncs the sprint store from GitHub, rate-limited to every 5 minutes.
func (b *Brain) maybeSyncSprint(ctx context.Context) {
	if b.sprintStore == nil {
		return
	}
	if time.Since(b.lastSync) < 5*time.Minute {
		return
	}

	for _, repo := range sprint.DefaultRepos {
		if err := b.sprintStore.Sync(ctx, repo); err != nil {
			b.log.Printf("sprint sync %s: %v", repo, err)
		}
	}
	b.lastSync = time.Now()
}

// identifyConstraint reads system state and returns the single most important constraint.
// Checked in priority order — first match wins.
func (b *Brain) identifyConstraint(ctx context.Context) Constraint {
	// 1. All drivers exhausted
	decision := b.dispatcher.router.Recommend("brain-constraint-check", "high")
	if decision.Skip {
		return Constraint{
			Type:        "all_drivers_down",
			Description: "all drivers exhausted — circuit breakers OPEN",
			Severity:    0,
		}
	}

	// 2. P0 bugs open
	if b.sprintStore != nil {
		dispatchable, err := b.sprintStore.NextDispatchable(ctx)
		if err == nil {
			for _, item := range dispatchable {
				if item.Priority == 0 {
					return Constraint{
						Type:        "p0_bugs",
						Description: fmt.Sprintf("P0 open: %s#%d — %s", item.Repo, item.IssueNum, item.Title),
						Severity:    0,
					}
				}
			}
		}
	}

	// 3. Idle agents (agents with 0 commits in recent runs, <10s avg duration)
	if b.profiles != nil {
		idleAgents := b.findIdleAgents(ctx)
		if len(idleAgents) > 0 {
			return Constraint{
				Type:        "idle_agents",
				Description: fmt.Sprintf("%d idle agents detected: %v", len(idleAgents), idleAgents),
				Severity:    1,
			}
		}
	}

	// 4. PRs waiting for review >30 min — check recent dispatch log
	stalePRs := b.findStalePRs(ctx)
	if stalePRs > 0 {
		return Constraint{
			Type:        "stale_prs",
			Description: fmt.Sprintf("%d PRs may be awaiting review", stalePRs),
			Severity:    1,
		}
	}

	// 5. No constraint — dispatch next sprint item
	if b.sprintStore != nil {
		dispatchable, err := b.sprintStore.NextDispatchable(ctx)
		if err == nil && len(dispatchable) > 0 {
			return Constraint{
				Type:        "none",
				Description: fmt.Sprintf("%d sprint items ready for dispatch", len(dispatchable)),
				Severity:    2,
			}
		}
	}

	return Constraint{Type: "none", Description: "system healthy, no actionable constraint", Severity: 2}
}

// highestLeverageAction scores candidate actions and returns the best one.
func (b *Brain) highestLeverageAction(ctx context.Context, constraint Constraint) *LeverageAction {
	switch constraint.Type {
	case "p0_bugs":
		return b.leverageForP0(ctx)
	case "idle_agents":
		return b.leverageForIdleAgents(ctx)
	case "stale_prs":
		return b.leverageForStalePRs(ctx)
	case "none":
		return b.leverageForNextSprint(ctx)
	default:
		return nil
	}
}

// leverageForP0 dispatches an SR at the P0 bug.
func (b *Brain) leverageForP0(ctx context.Context) *LeverageAction {
	if b.sprintStore == nil {
		return nil
	}
	dispatchable, err := b.sprintStore.NextDispatchable(ctx)
	if err != nil {
		return nil
	}

	for _, item := range dispatchable {
		if item.Priority == 0 {
			agent := b.srForSquad(item.Squad)
			if agent == "" {
				continue
			}
			return &LeverageAction{
				Agent:    agent,
				IssueNum: item.IssueNum,
				Repo:     item.Repo,
				Score:    10.0,
				Reason:   fmt.Sprintf("P0 bug: %s", item.Title),
			}
		}
	}
	return nil
}

// leverageForIdleAgents finds idle agents and assigns them sprint work.
func (b *Brain) leverageForIdleAgents(ctx context.Context) *LeverageAction {
	if b.sprintStore == nil {
		return nil
	}
	dispatchable, err := b.sprintStore.NextDispatchable(ctx)
	if err != nil || len(dispatchable) == 0 {
		return nil
	}

	// Just assign the highest-priority sprint item
	item := dispatchable[0]
	agent := b.srForSquad(item.Squad)
	if agent == "" {
		return nil
	}

	return &LeverageAction{
		Agent:    agent,
		IssueNum: item.IssueNum,
		Repo:     item.Repo,
		Score:    5.0,
		Reason:   fmt.Sprintf("idle agents detected, assigning sprint item: %s", item.Title),
	}
}

// leverageForStalePRs dispatches a reviewer.
func (b *Brain) leverageForStalePRs(ctx context.Context) *LeverageAction {
	return &LeverageAction{
		Agent:  "workspace-pr-review-agent",
		Score:  7.0,
		Reason: "PRs awaiting review — dispatching reviewer",
	}
}

// leverageForNextSprint dispatches the next sprint item by priority.
func (b *Brain) leverageForNextSprint(ctx context.Context) *LeverageAction {
	if b.sprintStore == nil {
		return nil
	}
	dispatchable, err := b.sprintStore.NextDispatchable(ctx)
	if err != nil || len(dispatchable) == 0 {
		return nil
	}

	item := dispatchable[0]
	agent := b.srForSquad(item.Squad)
	if agent == "" {
		return nil
	}

	return &LeverageAction{
		Agent:    agent,
		IssueNum: item.IssueNum,
		Repo:     item.Repo,
		Score:    3.0,
		Reason:   fmt.Sprintf("next sprint item (P%d): %s", item.Priority, item.Title),
	}
}

// executeLeverageAction dispatches the chosen action.
func (b *Brain) executeLeverageAction(ctx context.Context, action LeverageAction) {
	event := Event{
		Type:   EventType("brain.leverage"),
		Source: "brain",
		Payload: map[string]string{
			"reason":    action.Reason,
			"issue_num": fmt.Sprintf("%d", action.IssueNum),
			"repo":      action.Repo,
			"score":     fmt.Sprintf("%.1f", action.Score),
		},
		Priority: 1,
	}

	result, err := b.dispatcher.Dispatch(ctx, event, action.Agent, 1)
	if err != nil {
		b.log.Printf("leverage dispatch %s: %v", action.Agent, err)
		return
	}
	b.log.Printf("leverage: %s -> %s (score=%.1f, reason=%s)", action.Agent, result.Action, action.Score, action.Reason)
}

// findIdleAgents returns agent names that have been consistently idle.
func (b *Brain) findIdleAgents(ctx context.Context) []string {
	if b.profiles == nil {
		return nil
	}

	// Check known SR agents
	srAgents := []string{"kernel-sr", "cloud-sr", "shellforge-sr", "octi-pulpo-sr", "studio-sr"}
	var idle []string

	for _, agent := range srAgents {
		profile, err := b.profiles.GetProfile(ctx, agent)
		if err != nil || len(profile.RecentResults) == 0 {
			continue
		}
		if profile.ConsecutiveIdles >= 3 && profile.AvgDuration < 10 {
			idle = append(idle, agent)
		}
	}
	return idle
}

// findStalePRs checks recent dispatch log for PR review dispatches that might indicate stale PRs.
func (b *Brain) findStalePRs(ctx context.Context) int {
	rdb := b.dispatcher.RedisClient()
	ns := b.dispatcher.Namespace()

	raw, err := rdb.LRange(ctx, ns+":dispatch-log", 0, 49).Result()
	if err != nil || len(raw) == 0 {
		return 0
	}

	var staleCount int
	thirtyMinAgo := time.Now().UTC().Add(-30 * time.Minute)

	for _, r := range raw {
		var rec DispatchRecord
		if err := json.Unmarshal([]byte(r), &rec); err != nil {
			continue
		}
		// Look for PR-related dispatches that are old
		if containsAny(rec.Agent, "pr-review", "reviewer") && rec.Result == "dispatched" {
			ts, err := time.Parse(time.RFC3339, rec.Timestamp)
			if err == nil && ts.Before(thirtyMinAgo) {
				staleCount++
			}
		}
	}
	return staleCount
}

// srForSquad returns the SR agent name for a given squad.
func (b *Brain) srForSquad(squad string) string {
	mapping := map[string]string{
		"kernel":     "kernel-sr",
		"cloud":      "cloud-sr",
		"shellforge": "shellforge-sr",
		"octi-pulpo": "octi-pulpo-sr",
		"studio":     "studio-sr",
		"analytics":  "analytics-sr",
	}
	return mapping[squad]
}

// maybeProbeDriverHealth logs the current state of all driver circuits every 15
// minutes. When a driver has been OPEN for more than 30 minutes with no recent
// activity, the brain recommends action — giving human operators or the bash
// driver-health scripts visibility into stale circuits. We deliberately do not
// exec driver CLIs here; that is the responsibility of run-agent.sh and
// driver-health.sh which understand each driver's specific probe command.
func (b *Brain) maybeProbeDriverHealth() {
	if time.Since(b.lastProbeAt) < 15*time.Minute {
		return
	}
	b.lastProbeAt = time.Now()

	healthDir := b.dispatcher.router.HealthDir()
	drivers := routing.DiscoverDrivers(healthDir)
	if len(drivers) == 0 {
		return
	}

	var openDrivers []string
	for _, driver := range drivers {
		h := routing.ReadDriverHealth(healthDir, driver)
		if h.CircuitState != "OPEN" {
			continue
		}
		openDrivers = append(openDrivers, driver)
		age := openedAge(h.OpenedAt)
		action := routing.RecommendAction(h)
		b.log.Printf("driver probe: %s OPEN (age=%s) — %s", driver, formatDuration(age), action)
	}

	if len(openDrivers) == 0 {
		b.log.Printf("driver probe: all %d drivers healthy", len(drivers))
	} else {
		b.log.Printf("driver probe: %d/%d drivers OPEN: %v", len(openDrivers), len(drivers), openDrivers)
	}
}

// openedAge returns the time elapsed since a driver's circuit was opened.
func openedAge(openedAt string) time.Duration {
	if openedAt == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, openedAt)
	if err != nil {
		return 0
	}
	return time.Since(t)
}

// formatDuration formats a duration as a compact human string.
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
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

	stats := map[string]interface{}{
		"queue_depth":    depth,
		"pending_agents": agents,
		"worker_ok":      okCount,
		"worker_fail":    failCount,
		"chain_count":    len(b.chains),
		"tick_interval":  b.tickInterval.String(),
	}

	// Add constraint info if sprint store is available
	if b.sprintStore != nil {
		constraint := b.identifyConstraint(ctx)
		stats["constraint_type"] = constraint.Type
		stats["constraint_desc"] = constraint.Description
		stats["constraint_severity"] = constraint.Severity
	}

	return stats
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
