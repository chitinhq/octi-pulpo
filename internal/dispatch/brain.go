package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
	"github.com/AgentGuardHQ/octi-pulpo/internal/standup"
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
//   - Driver health probe: ping each driver every 15 min to detect stale state
//   - Slack notifications: periodic budget dashboard + driver state change alerts
//   - Daily standup: post unified squad standup to Slack once per day
//   - Self-heal: detect stuck agents (triage flag) + inactive squads, alert CTO
type Brain struct {
	dispatcher        *Dispatcher
	chains            ChainConfig
	tickInterval      time.Duration
	probeInterval     time.Duration
	log               *log.Logger
	sprintStore       *sprint.Store
	standupStore      *standup.Store
	profiles          *ProfileStore
	notifier          *Notifier
	lastSync          time.Time
	lastProbe         time.Time
	lastDashboard     time.Time
	dashboardPeriod   time.Duration
	lastStandupDate   string // YYYY-MM-DD, guards once-per-day posting
	driversWereDown   bool   // tracks transition for edge-triggered alerts

	// Self-heal dedup: tracks when each agent/squad was last alerted to avoid
	// flooding Slack on every tick. Keys are agent names or squad names.
	stuckAgentAlerted    map[string]time.Time
	inactiveSquadAlerted map[string]time.Time
}

// NewBrain creates a dispatch brain.
func NewBrain(dispatcher *Dispatcher, chains ChainConfig) *Brain {
	return &Brain{
		dispatcher:           dispatcher,
		chains:               chains,
		tickInterval:         60 * time.Second,
		probeInterval:        15 * time.Minute,
		dashboardPeriod:      4 * time.Hour,
		log:                  log.New(os.Stderr, "brain: ", log.LstdFlags),
		stuckAgentAlerted:    make(map[string]time.Time),
		inactiveSquadAlerted: make(map[string]time.Time),
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

// SetStandupStore enables daily standup Slack posting.
func (b *Brain) SetStandupStore(s *standup.Store) {
	b.standupStore = s
}

// SetNotifier enables Slack notifications for driver state changes and periodic dashboards.
func (b *Brain) SetNotifier(n *Notifier) {
	b.notifier = n
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

	// 2. Probe driver health every 15 minutes
	b.maybeProbeDrivers(ctx)

	// 3. Legacy checks
	b.checkBackpressureRecovery(ctx)
	b.checkQueueHealth(ctx)
	b.checkStalledDispatches(ctx)

	// 4. Periodic Slack dashboard
	b.maybePostDashboard(ctx)

	// 5. Daily standup (once per calendar day)
	b.maybePostDailyStandup(ctx)

	// 6. Self-heal: stuck agents + inactive squads
	b.maybeSelfHeal(ctx)

	// 7. Constraint-driven dispatch (if sprint store is available)
	if b.sprintStore != nil {
		constraint := b.identifyConstraint(ctx)
		b.maybeNotifyConstraintChange(ctx, constraint)
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

// maybeProbeDrivers runs driver health probes on the configured interval.
func (b *Brain) maybeProbeDrivers(ctx context.Context) {
	if time.Since(b.lastProbe) < b.probeInterval {
		return
	}
	b.ProbeDrivers(ctx)
	b.lastProbe = time.Now()
}

// ProbeDrivers checks all discovered drivers with a lightweight CLI probe.
// CLOSED/HALF drivers: confirms they are still reachable.
// OPEN drivers: checks whether budget has recovered, closes circuit if so.
func (b *Brain) ProbeDrivers(ctx context.Context) {
	healthDir := b.dispatcher.router.HealthDir()
	drivers := routing.DiscoverDrivers(healthDir)
	if len(drivers) == 0 {
		return
	}
	b.log.Printf("probing %d drivers for health", len(drivers))
	for _, driver := range drivers {
		health := routing.ReadDriverHealth(healthDir, driver)
		ok, output := b.probeOneDriver(ctx, driver)
		if ok {
			if health.CircuitState == "OPEN" {
				b.log.Printf("driver %s recovered — closing circuit", driver)
				if err := routing.CloseCircuit(healthDir, driver); err != nil {
					b.log.Printf("close circuit %s: %v", driver, err)
				}
			}
		} else {
			if health.CircuitState != "OPEN" {
				// Check whether the failure is a credit error vs an unavailability
				if probedDriver, found := routing.DetectExhaustedDriver(output); found {
					reportDriver := driver
					if probedDriver != "unknown" {
						reportDriver = probedDriver
					}
					b.log.Printf("driver %s probe: credit error detected — opening circuit", reportDriver)
					if err := routing.OpenCircuit(healthDir, reportDriver); err != nil {
						b.log.Printf("open circuit %s: %v", reportDriver, err)
					}
				} else {
					b.log.Printf("driver %s probe: unreachable (not a credit error, skipping circuit change)", driver)
				}
			}
		}
	}
}

// driverProbeCommands maps driver names to the lightweight CLI command used
// to verify reachability. Commands are intentionally non-destructive (version
// checks or auth status only — no token consumption).
var driverProbeCommands = map[string][]string{
	"claude-code": {"claude", "--version"},
	"copilot":     {"gh", "auth", "status"},
	"codex":       {"codex", "--version"},
	"gemini":      {"gemini", "--version"},
	"goose":       {"goose", "--version"},
	"ollama":      {"ollama", "--version"},
	"nemotron":    {"ollama", "--version"},
	"openclaw":    {"openclaw", "--version"},
}

// probeOneDriver runs a lightweight availability check for the given driver.
// Returns (true, "") on success, (false, combinedOutput) on failure.
func (b *Brain) probeOneDriver(ctx context.Context, driver string) (bool, string) {
	args, known := driverProbeCommands[driver]
	if !known {
		// Unknown driver: assume healthy (no probe command available)
		return true, ""
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		return false, output
	}
	return true, output
}

// maybePostDashboard posts a Slack budget dashboard at most once per dashboardPeriod.
// It reads live driver health and cumulative worker counters from Redis.
func (b *Brain) maybePostDashboard(ctx context.Context) {
	if b.notifier == nil || !b.notifier.Enabled() {
		return
	}
	if time.Since(b.lastDashboard) < b.dashboardPeriod {
		return
	}

	drivers := b.dispatcher.router.AllHealth()
	rdb := b.dispatcher.RedisClient()
	ns := b.dispatcher.Namespace()

	okStr, _ := rdb.Get(ctx, ns+":worker-ok").Result()
	failStr, _ := rdb.Get(ctx, ns+":worker-fail").Result()
	ok, _ := strconv.ParseInt(okStr, 10, 64)
	fail, _ := strconv.ParseInt(failStr, 10, 64)

	if err := b.notifier.PostBudgetDashboard(ctx, drivers, ok, fail); err != nil {
		b.log.Printf("slack dashboard: %v", err)
		return
	}
	b.lastDashboard = time.Now()
}

// maybePostDailyStandup posts the unified squad standup to Slack once per calendar day.
// It only fires when the standup store has at least one entry and Slack is configured.
func (b *Brain) maybePostDailyStandup(ctx context.Context) {
	if b.notifier == nil || !b.notifier.Enabled() {
		return
	}
	if b.standupStore == nil {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	if b.lastStandupDate == today {
		return
	}

	entries, err := b.standupStore.Daily(ctx)
	if err != nil {
		b.log.Printf("standup daily read: %v", err)
		return
	}
	if len(entries) == 0 {
		return // nothing to post yet
	}

	if err := b.notifier.PostDailyStandup(ctx, entries); err != nil {
		b.log.Printf("slack standup: %v", err)
		return
	}
	b.lastStandupDate = today
}

// maybeSelfHeal runs Phase 2 adaptive recovery checks on every tick:
//
//  1. Stuck agents: agents with TriageFlag=true get a Slack alert (at most once per 12h).
//  2. Inactive squads: squads with no dispatch activity in the last 24h get a Slack alert
//     (at most once per 24h). Activity is determined from the dispatch log.
func (b *Brain) maybeSelfHeal(ctx context.Context) {
	if b.profiles == nil {
		return
	}

	b.checkStuckAgents(ctx)
	b.checkInactiveSquads(ctx)
}

// checkStuckAgents scans all agent profiles for triage-flagged agents and fires
// a one-time Slack alert (per 12h window) for each.
func (b *Brain) checkStuckAgents(ctx context.Context) {
	profiles, err := b.profiles.AllProfiles(ctx)
	if err != nil {
		b.log.Printf("self-heal: list profiles: %v", err)
		return
	}

	for _, p := range profiles {
		if !p.TriageFlag {
			continue
		}
		// Deduplicate: alert at most once per 12h per agent.
		if last, ok := b.stuckAgentAlerted[p.Name]; ok && time.Since(last) < 12*time.Hour {
			continue
		}
		b.log.Printf("self-heal: stuck agent %s (%d consecutive failures) — alerting", p.Name, p.ConsecutiveFails)
		b.stuckAgentAlerted[p.Name] = time.Now()

		if b.notifier != nil && b.notifier.Enabled() {
			if err := b.notifier.PostStuckAgentAlert(ctx, p.Name, p.ConsecutiveFails); err != nil {
				b.log.Printf("self-heal: slack alert for %s: %v", p.Name, err)
			}
		}
	}
}

// checkInactiveSquads inspects the recent dispatch log and alerts when a squad
// has had no dispatch activity for more than 24 hours.
func (b *Brain) checkInactiveSquads(ctx context.Context) {
	records, err := b.dispatcher.RecentDispatches(ctx, 200)
	if err != nil || len(records) == 0 {
		return
	}

	// Build the most recent dispatch timestamp per squad.
	lastActivity := make(map[string]time.Time)
	for _, rec := range records {
		squad := inferSquad(rec.Agent)
		if squad == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.Timestamp)
		if err != nil {
			continue
		}
		if ts.After(lastActivity[squad]) {
			lastActivity[squad] = ts
		}
	}

	threshold := 24 * time.Hour
	for squad, last := range lastActivity {
		idle := time.Since(last)
		if idle < threshold {
			continue
		}
		// Deduplicate: alert at most once per 24h per squad.
		if alertedAt, ok := b.inactiveSquadAlerted[squad]; ok && time.Since(alertedAt) < 24*time.Hour {
			continue
		}
		idleHours := int(idle.Hours())
		b.log.Printf("self-heal: inactive squad %s (idle %dh) — alerting", squad, idleHours)
		b.inactiveSquadAlerted[squad] = time.Now()

		if b.notifier != nil && b.notifier.Enabled() {
			if err := b.notifier.PostInactiveSquadAlert(ctx, squad, idleHours); err != nil {
				b.log.Printf("self-heal: slack alert for squad %s: %v", squad, err)
			}
		}
	}
}

// maybeNotifyConstraintChange fires edge-triggered Slack alerts when driver
// availability transitions between healthy and all-exhausted states.
func (b *Brain) maybeNotifyConstraintChange(ctx context.Context, constraint Constraint) {
	if b.notifier == nil || !b.notifier.Enabled() {
		return
	}
	nowDown := constraint.Type == "all_drivers_down"
	if nowDown && !b.driversWereDown {
		b.driversWereDown = true
		if err := b.notifier.PostDriversDown(ctx, constraint.Description); err != nil {
			b.log.Printf("slack drivers-down: %v", err)
		}
	} else if !nowDown && b.driversWereDown {
		b.driversWereDown = false
		if err := b.notifier.PostDriversRecovered(ctx); err != nil {
			b.log.Printf("slack drivers-recovered: %v", err)
		}
	}
}

// identifyConstraint reads system state and returns the single most important constraint.
// Checked in priority order — first match wins.
func (b *Brain) identifyConstraint(ctx context.Context) Constraint {
	// 1. All drivers exhausted (within current budget policy; "high"/API tier is never
	// assumed available automatically — use DynamicBudget to stay within economics)
	decision := b.dispatcher.router.Recommend("brain-constraint-check", b.dispatcher.router.DynamicBudget())
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

	// 2.5. P0 items with open PRs ready to merge — higher priority than idle agents.
	// Prevents the brain from dispatching SR agents to re-implement work that
	// already has a passing PR waiting in the queue.
	if b.sprintStore != nil {
		mergeable, err := b.sprintStore.NextMergeable(ctx)
		if err == nil {
			for _, item := range mergeable {
				if item.Priority == 0 {
					return Constraint{
						Type:        "p0_prs_ready",
						Description: fmt.Sprintf("P0 PR ready: %s#%d (PR #%d) — %s", item.Repo, item.IssueNum, item.PRNumber, item.Title),
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
	case "p0_prs_ready":
		return b.leverageForP0PRsMerge(ctx)
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

// leverageForP0PRsMerge dispatches pr-merger-agent at the highest-priority item
// that has an open PR, preventing duplicate SR dispatches for in-flight work.
func (b *Brain) leverageForP0PRsMerge(ctx context.Context) *LeverageAction {
	if b.sprintStore == nil {
		return nil
	}
	mergeable, err := b.sprintStore.NextMergeable(ctx)
	if err != nil || len(mergeable) == 0 {
		return nil
	}
	for _, item := range mergeable {
		if item.Priority == 0 {
			return &LeverageAction{
				Agent:    "pr-merger-agent",
				IssueNum: item.IssueNum,
				Repo:     item.Repo,
				Score:    9.0,
				Reason:   fmt.Sprintf("P0 PR #%d ready to merge: %s", item.PRNumber, item.Title),
			}
		}
	}
	return nil
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

// checkBackpressureRecovery looks for agents that were queued due to
// driver exhaustion. If drivers have recovered, re-dispatch them.
func (b *Brain) checkBackpressureRecovery(ctx context.Context) {
	depth, err := b.dispatcher.PendingCount(ctx)
	if err != nil || depth == 0 {
		return
	}

	// Check if drivers are healthy now — use dynamic budget to avoid API-tier false positives
	decision := b.dispatcher.router.Recommend("brain-check", b.dispatcher.router.DynamicBudget())
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
