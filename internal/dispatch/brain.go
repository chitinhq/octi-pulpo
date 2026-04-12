package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/coordination"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/chitinhq/octi-pulpo/internal/sprint"
	"github.com/chitinhq/octi-pulpo/internal/standup"
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
	TaskType string // maps to adapter task types: "bugfix", "code-gen", "config", etc.
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
	notifier          *NtfyNotifier
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

	// Swarm dispatch dedup: tracks recent dispatch attempts to avoid
	// retrying the same issue every tick. Key: "repo#num", value: last attempt.
	swarmAttempted map[string]time.Time

	// Adapters for task-based dispatch (Clawta + GH Actions).
	adapters []Adapter

	// ghToken is used for label state machine operations on GitHub issues.
	ghToken string

	// Swarm assembly line
	queueMachine   *QueueMachine
	stagger        *StaggerTracker
	modelRouter    *ModelRouter
	escalation     *EscalationManager
	claudeAdapter  *ClaudeCodeAdapter
	copilotAdapter *CopilotCLIAdapter

	// Config-driven dispatch
	platformConfig *PlatformConfigHolder
	skipList       *SkipList
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
		swarmAttempted:       make(map[string]time.Time),
	}
}

// SetAdapters registers task adapters (Clawta, GH Actions) for direct dispatch.
func (b *Brain) SetAdapters(adapters ...Adapter) { b.adapters = adapters }

// SetGitHubToken sets the token used for label state machine operations.
func (b *Brain) SetGitHubToken(token string) { b.ghToken = token }

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

// SetNotifier enables ntfy push notifications for driver state changes and periodic dashboards.
func (b *Brain) SetNotifier(n *NtfyNotifier) {
	b.notifier = n
}

// SetQueueMachine wires the swarm queue state machine into the brain.
func (b *Brain) SetQueueMachine(qm *QueueMachine) { b.queueMachine = qm }

// SetStagger wires the stagger tracker into the brain.
func (b *Brain) SetStagger(s *StaggerTracker) { b.stagger = s }

// SetModelRouter wires the model router into the brain.
func (b *Brain) SetModelRouter(mr *ModelRouter) { b.modelRouter = mr }

// SetEscalationManager wires the escalation manager into the brain.
func (b *Brain) SetEscalationManager(em *EscalationManager) { b.escalation = em }

// SetClaudeCodeAdapter wires the Claude Code CLI adapter into the brain.
func (b *Brain) SetClaudeCodeAdapter(a *ClaudeCodeAdapter) { b.claudeAdapter = a }

// SetCopilotCLIAdapter wires the Copilot CLI adapter into the brain.
func (b *Brain) SetCopilotCLIAdapter(a *CopilotCLIAdapter) { b.copilotAdapter = a }

// SetPlatformConfig wires the platform config holder into the brain.
func (b *Brain) SetPlatformConfig(pc *PlatformConfigHolder) { b.platformConfig = pc }

// SetSkipList wires the skip list into the brain.
func (b *Brain) SetSkipList(sl *SkipList) { b.skipList = sl }

// Run starts the brain evaluation loop. Blocks until context is cancelled.
// Set OCTI_BRAIN_DISPATCH=0 to disable CLI agent dispatch (use pipeline instead).
func (b *Brain) Run(ctx context.Context) error {
	if os.Getenv("OCTI_BRAIN_DISPATCH") == "0" {
		b.log.Printf("brain dispatch DISABLED (OCTI_BRAIN_DISPATCH=0) — pipeline handles work")
	}
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

	// 6b. Progress gap detection: warn on active claims with no snapshots for >2 min
	b.checkProgressGaps(ctx)

	// 6c. Swarm assembly line: queue-aware dispatch with stagger + time-gating
	if b.queueMachine != nil {
		b.maybeRunSwarmCycle(ctx)
	}

	// 7. Constraint-driven dispatch (if sprint store is available)
	if b.sprintStore != nil {
		constraint := b.identifyConstraint(ctx)
		b.maybeNotifyConstraintChange(ctx, constraint)
		if constraint.Type != "all_drivers_down" {
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

// maybeRunSwarmCycle runs one swarm dispatch cycle if the stagger window allows.
// Scans sprint items for dispatchable issues, classifies by queue priority,
// and dispatches via dispatch.sh to the selected CLI platform.
func (b *Brain) maybeRunSwarmCycle(ctx context.Context) {
	if b.queueMachine == nil || b.stagger == nil || b.sprintStore == nil {
		return
	}

	// If platform config is available, use config-driven dispatch.
	if b.platformConfig != nil {
		b.configDrivenDispatch(ctx)
		return
	}

	now := time.Now().UTC()
	hour := now.Hour()
	isActiveHours := hour >= 0 && hour < 4 // 00:00-04:00 UTC = Jared's active hours

	claudeAvail := !isActiveHours &&
		b.stagger.IsAvailable("claude", now) &&
		b.stagger.IsUnderDailyCap("claude", now)
	copilotAvail := b.stagger.IsAvailable("copilot", now) &&
		b.stagger.IsUnderDailyCap("copilot", now)

	if !claudeAvail && !copilotAvail {
		return
	}

	platform := b.stagger.NextPlatform(copilotAvail, claudeAvail)
	if platform == "" {
		return
	}

	// Scan sprint items and classify by queue.
	items, err := b.sprintStore.GetAll(ctx)
	if err != nil {
		b.log.Printf("swarm: sprint GetAll: %v", err)
		return
	}

	// Count items per queue and collect dispatchable candidates.
	type candidate struct {
		item  sprint.SprintItem
		queue Queue
	}
	queueCounts := make(map[Queue]int)
	var candidates []candidate

	for _, item := range items {
		// Skip items that are already claimed, done, or have open PRs.
		if item.Status == "claimed" || item.Status == "done" || item.Status == "pr_open" || item.Status == "blocked" {
			continue
		}
		q := b.queueMachine.ClassifyQueue(item.Labels)
		if q == QueueDone || q == QueueHuman || q == QueueInProgress {
			continue
		}
		queueCounts[q]++
		candidates = append(candidates, candidate{item: item, queue: q})
	}

	if len(candidates) == 0 {
		return
	}

	// Pick the highest-priority queue, then the highest-priority item in it.
	targetQueue := b.queueMachine.PickHighestPriority(queueCounts)

	var best *candidate
	for i := range candidates {
		c := &candidates[i]
		if c.queue != targetQueue {
			continue
		}
		// Skip issues attempted in the last 30 minutes.
		issueKey := fmt.Sprintf("%s#%d", c.item.Repo, c.item.IssueNum)
		if last, ok := b.swarmAttempted[issueKey]; ok && time.Since(last) < 30*time.Minute {
			continue
		}
		if best == nil || c.item.Priority < best.item.Priority {
			best = c
		}
	}
	if best == nil {
		return
	}

	// Record attempt before dispatching.
	b.swarmAttempted[fmt.Sprintf("%s#%d", best.item.Repo, best.item.IssueNum)] = now

	// Determine queue name and model for dispatch.
	queueName := queueNameStr(targetQueue)
	complexity := b.queueMachine.ComplexityFromLabels(best.item.Labels)
	var model string
	if platform == "claude" {
		model = b.modelRouter.ClaudeModel(complexity)
	} else {
		model = b.modelRouter.CopilotModel(complexity)
	}

	// Extract repo short name (e.g., "chitin" from "chitinhq/chitin").
	repoShort := best.item.Repo
	if idx := strings.LastIndex(repoShort, "/"); idx >= 0 {
		repoShort = repoShort[idx+1:]
	}

	b.log.Printf("swarm: dispatching %s/%s#%d (%s) via %s/%s",
		best.item.Repo, queueName, best.item.IssueNum, complexity, platform, model)

	// Dispatch via dispatch.sh — it handles worktree, labels, telemetry, post-validation.
	home, _ := os.UserHomeDir()
	dispatchScript := filepath.Join(home, "workspace", "octi", "scripts", "swarm", "dispatch.sh")

	// Use background context — dispatch runs in a goroutine and outlives this tick.
	dispatchCtx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)

	cmd := exec.CommandContext(dispatchCtx, "bash", dispatchScript,
		platform, repoShort, strconv.Itoa(best.item.IssueNum), queueName, model)
	cmd.Env = os.Environ()
	cmd.Dir = filepath.Join(home, "workspace")

	// Run in background so the brain loop doesn't block.
	// Only record stagger on success so failed pre-checks don't waste cooldown.
	dispatchPlatform := platform
	go func() {
		defer cancel()
		out, err := cmd.CombinedOutput()
		output := strings.TrimSpace(string(out))
		if err != nil {
			reason := extractDispatchReason(output)
			// Suppress notifications for expected pre-check blocks.
			quiet := isExpectedBlock(reason)
			if quiet {
				b.log.Printf("swarm: %s/%s#%d skipped: %s",
					best.item.Repo, queueName, best.item.IssueNum, reason)
			} else {
				b.log.Printf("swarm: dispatch %s/%s#%d failed: %s",
					best.item.Repo, queueName, best.item.IssueNum, reason)
				if b.notifier != nil {
					b.notifier.Post(dispatchCtx, "swarm dispatch FAILED",
						fmt.Sprintf("%s#%d (%s/%s): %s", repoShort, best.item.IssueNum, platform, model, reason), 4)
				}
			}
		} else {
			b.log.Printf("swarm: dispatch %s/%s#%d succeeded via %s/%s",
				best.item.Repo, queueName, best.item.IssueNum, platform, model)
			b.stagger.RecordDispatch(dispatchPlatform, time.Now())
			if b.notifier != nil {
				b.notifier.Post(dispatchCtx, "swarm dispatch OK", fmt.Sprintf("%s#%d (%s/%s → %s)",
					repoShort, best.item.IssueNum, platform, model, queueName), 3)
			}
		}
	}()
}

func (b *Brain) configDrivenDispatch(ctx context.Context) {
	cfg := b.platformConfig.Get()
	now := time.Now().UTC()

	// Expire old skip list entries.
	if b.skipList != nil {
		b.skipList.ExpireOld()
	}

	// Scan sprint items and collect dispatchable candidates.
	items, err := b.sprintStore.GetAll(ctx)
	if err != nil {
		b.log.Printf("swarm: sprint GetAll: %v", err)
		return
	}

	type candidate struct {
		item  sprint.SprintItem
		queue Queue
	}
	var candidates []candidate

	for _, item := range items {
		if item.Status == "claimed" || item.Status == "done" || item.Status == "pr_open" || item.Status == "blocked" {
			continue
		}
		q := b.queueMachine.ClassifyQueue(item.Labels)
		if q == QueueDone || q == QueueHuman || q == QueueInProgress {
			continue
		}

		issueKey := fmt.Sprintf("%s#%d", repoShortName(item.Repo), item.IssueNum)

		// Skip if in skip list.
		if b.skipList != nil && b.skipList.IsSkipped(issueKey) {
			continue
		}
		// Skip if attempted recently (30 min dedup).
		if last, ok := b.swarmAttempted[issueKey]; ok && time.Since(last) < 30*time.Minute {
			continue
		}

		candidates = append(candidates, candidate{item: item, queue: q})
	}

	if len(candidates) == 0 {
		return
	}

	// Pick the highest-priority queue, then the best item in it.
	queueCounts := make(map[Queue]int)
	for _, c := range candidates {
		queueCounts[c.queue]++
	}
	targetQueue := b.queueMachine.PickHighestPriority(queueCounts)

	var best *candidate
	for i := range candidates {
		c := &candidates[i]
		if c.queue != targetQueue {
			continue
		}
		if best == nil || c.item.Priority < best.item.Priority {
			best = c
		}
	}
	if best == nil {
		return
	}

	queueName := queueNameStr(targetQueue)

	// Walk platforms in priority order, find first match.
	var chosenPlatform string
	var chosenModel string
	for _, name := range cfg.Priority {
		entry := cfg.Platforms[name]
		if !entry.Enabled {
			continue
		}
		if !entry.AcceptsQueue(queueName) {
			continue
		}
		if !b.stagger.IsAvailable(name, now) {
			continue
		}
		if !b.stagger.IsUnderDailyCap(name, now) {
			continue
		}
		chosenPlatform = name
		chosenModel = entry.Model
		break
	}

	if chosenPlatform == "" {
		// No platform matched — record rejection.
		issueKey := fmt.Sprintf("%s#%d", best.item.Repo, best.item.IssueNum)
		if b.skipList != nil {
			b.skipList.RecordRejection(issueKey)
			if b.skipList.IsSkipped(issueKey) {
				b.log.Printf("swarm: %s added to skip list — no platform accepts queue %s", issueKey, queueName)
				if b.notifier != nil {
					b.notifier.Post(ctx, "Unroutable issue",
						fmt.Sprintf("%s (queue: %s) — skipped for 24h", issueKey, queueName), NtfyPriorityLow)
				}
			}
		}
		return
	}

	// Record attempt and dispatch.
	issueKey := fmt.Sprintf("%s#%d", best.item.Repo, best.item.IssueNum)
	b.swarmAttempted[issueKey] = now

	repoShort := best.item.Repo
	if idx := strings.LastIndex(repoShort, "/"); idx >= 0 {
		repoShort = repoShort[idx+1:]
	}

	b.log.Printf("swarm: dispatching %s/%s#%d via %s/%s",
		best.item.Repo, queueName, best.item.IssueNum, chosenPlatform, chosenModel)

	home, _ := os.UserHomeDir()
	dispatchScript := filepath.Join(home, "workspace", "octi", "scripts", "swarm", "dispatch.sh")
	dispatchCtx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	cmd := exec.CommandContext(dispatchCtx, "bash", dispatchScript,
		chosenPlatform, repoShort, strconv.Itoa(best.item.IssueNum), queueName, chosenModel)
	cmd.Env = os.Environ()
	cmd.Dir = filepath.Join(home, "workspace")

	platform := chosenPlatform
	go func() {
		defer cancel()
		out, err := cmd.CombinedOutput()
		output := strings.TrimSpace(string(out))
		if err != nil {
			reason := extractDispatchReason(output)
			quiet := isExpectedBlock(reason)
			if quiet {
				b.log.Printf("swarm: %s/%s#%d skipped: %s",
					best.item.Repo, queueName, best.item.IssueNum, reason)
			} else {
				b.log.Printf("swarm: dispatch %s/%s#%d failed: %s",
					best.item.Repo, queueName, best.item.IssueNum, reason)
				if b.notifier != nil {
					b.notifier.Post(dispatchCtx, "swarm dispatch FAILED",
						fmt.Sprintf("%s#%d (%s/%s): %s", repoShort, best.item.IssueNum, platform, chosenModel, reason), 4)
				}
			}
		} else {
			b.log.Printf("swarm: dispatch %s/%s#%d succeeded via %s/%s",
				best.item.Repo, queueName, best.item.IssueNum, platform, chosenModel)
			b.stagger.RecordDispatch(platform, time.Now())
			if b.notifier != nil {
				b.notifier.Post(dispatchCtx, "swarm dispatch OK", fmt.Sprintf("%s#%d (%s/%s → %s)",
					repoShort, best.item.IssueNum, platform, chosenModel, queueName), 3)
			}
		}
	}()
}

// queueNameStr maps Queue constants to the string names dispatch.sh expects.
func queueNameStr(q Queue) string {
	switch q {
	case QueueGroom:
		return "groom"
	case QueueIntake:
		return "intake"
	case QueueBuild:
		return "build"
	case QueueValidate:
		return "validate"
	default:
		return "intake"
	}
}

// extractDispatchReason pulls the human-readable failure reason from dispatch.sh output.
// Looks for "PRE-DISPATCH FAIL:" or "ABORT:" lines. Falls back to last non-empty line.
func extractDispatchReason(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PRE-DISPATCH FAIL: ") {
			return strings.TrimPrefix(line, "PRE-DISPATCH FAIL: ")
		}
		if strings.HasPrefix(line, "POST-DISPATCH FAIL: ") {
			return strings.TrimPrefix(line, "POST-DISPATCH FAIL: ")
		}
	}
	// Fallback: last non-empty line
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "unknown error"
}

// isExpectedBlock returns true for pre-check failures that are normal and
// shouldn't generate notifications (interactive session, already planned, etc).
func isExpectedBlock(reason string) bool {
	quietPatterns := []string{
		"interactive Claude session detected",
		"already has 'planned' label",
		"already has 'implemented' label",
		"already validated",
		"already claimed",
		"budget check failed",
	}
	for _, p := range quietPatterns {
		if strings.Contains(reason, p) {
			return true
		}
	}
	return false
}

// lastLines returns the last n lines of a string.
func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
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
	"copilot":     {"copilot", "--version"},
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

// maybePostDashboard posts a Slack status digest at most once per dashboardPeriod.
// When a sprint store is wired, it sends PostSprintDigest (drivers + pass rate +
// sprint progress + open PRs + blockers). Otherwise it falls back to the
// simpler PostBudgetDashboard (drivers + pass rate only).
func (b *Brain) maybePostDashboard(ctx context.Context) {
	if b.notifier == nil || !b.notifier.Enabled() {
		return
	}
	if time.Since(b.lastDashboard) < b.dashboardPeriod {
		return
	}

	// NOTE: CLI driver health is deprecated (see #153). Pass nil to omit from digest.
	rdb := b.dispatcher.RedisClient()
	ns := b.dispatcher.Namespace()

	okStr, _ := rdb.Get(ctx, ns+":worker-ok").Result()
	failStr, _ := rdb.Get(ctx, ns+":worker-fail").Result()
	ok, _ := strconv.ParseInt(okStr, 10, 64)
	fail, _ := strconv.ParseInt(failStr, 10, 64)

	if b.sprintStore != nil {
		items, err := b.sprintStore.GetAll(ctx)
		if err != nil {
			b.log.Printf("sprint digest: get all: %v", err)
			// fall through to budget-only dashboard
		} else {
			if err := b.notifier.PostSprintDigest(ctx, nil, ok, fail, items); err != nil {
				b.log.Printf("slack sprint digest: %v", err)
				return
			}
			b.lastDashboard = time.Now()
			return
		}
	}

	if err := b.notifier.PostBudgetDashboard(ctx, nil, ok, fail); err != nil {
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

// checkProgressGaps scans active claims and warns if any worker has not published
// a progress snapshot in the last 2 minutes. This detects silently stuck workers.
func (b *Brain) checkProgressGaps(ctx context.Context) {
	coord := b.dispatcher.Coord()
	if coord == nil {
		return
	}
	claims, err := coord.ActiveClaims(ctx)
	if err != nil {
		return
	}
	rdb := b.dispatcher.RedisClient()
	ns := b.dispatcher.Namespace()
	for _, c := range claims {
		gap, err := coordination.DetectGap(ctx, rdb, ns, c.ClaimID, 2*time.Minute)
		if err != nil {
			continue
		}
		if gap {
			// Deduplicate: only log once per claim per 10 min window.
			key := "progress-gap:" + c.ClaimID
			if last, ok := b.stuckAgentAlerted[key]; ok && time.Since(last) < 10*time.Minute {
				continue
			}
			b.log.Printf("progress gap: worker %s (claim %s) has no snapshot in >2 min", c.AgentID, c.ClaimID)
			b.stuckAgentAlerted[key] = time.Now()
		}
	}
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
// Suppressed when API adapters are available — adapter dispatch activity isn't
// recorded in the legacy dispatch log, so squads would always appear idle.
func (b *Brain) checkInactiveSquads(ctx context.Context) {
	if len(b.adapters) > 0 {
		return
	}
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
// Suppressed when API adapters are available — CLI driver state is noise
// when dispatch flows through Clawta/GH Actions.
func (b *Brain) maybeNotifyConstraintChange(ctx context.Context, constraint Constraint) {
	if b.notifier == nil || !b.notifier.Enabled() {
		return
	}
	// When adapters handle dispatch, CLI driver state is informational only.
	if len(b.adapters) > 0 {
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
	// 1. All CLI drivers exhausted — but only block if no API adapters exist.
	// API adapters (Clawta, GH Actions) dispatch independently of CLI driver health.
	decision := b.dispatcher.router.Recommend("brain-constraint-check", b.dispatcher.router.DynamicBudget())
	if decision.Skip && len(b.adapters) == 0 {
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
			// Check skip list before dispatching.
			issueKey := fmt.Sprintf("%s#%d", repoShortName(item.Repo), item.IssueNum)
			if b.skipList != nil && b.skipList.IsSkipped(issueKey) {
				continue
			}
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
				TaskType: "bugfix",
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

	// Walk dispatchable items, skip any in the skip list.
	for _, item := range dispatchable {
		issueKey := fmt.Sprintf("%s#%d", repoShortName(item.Repo), item.IssueNum)
		if b.skipList != nil && b.skipList.IsSkipped(issueKey) {
			continue
		}
		agent := b.srForSquad(item.Squad)
		if agent == "" {
			continue
		}
		return &LeverageAction{
			Agent:    agent,
			IssueNum: item.IssueNum,
			Repo:     item.Repo,
			Score:    5.0,
			Reason:   fmt.Sprintf("idle agents detected, assigning sprint item: %s", item.Title),
			TaskType: inferTaskType(item.Title),
		}
	}
	return nil
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

	for _, item := range dispatchable {
		issueKey := fmt.Sprintf("%s#%d", repoShortName(item.Repo), item.IssueNum)
		if b.skipList != nil && b.skipList.IsSkipped(issueKey) {
			continue
		}
		agent := b.srForSquad(item.Squad)
		if agent == "" {
			continue
		}
		return &LeverageAction{
			Agent:    agent,
			IssueNum: item.IssueNum,
			Repo:     item.Repo,
			Score:    3.0,
			Reason:   fmt.Sprintf("next sprint item (P%d): %s", item.Priority, item.Title),
			TaskType: inferTaskType(item.Title),
		}
	}
	return nil
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

	if os.Getenv("OCTI_BRAIN_DISPATCH") == "0" {
		b.log.Printf("leverage: %s -> BLOCKED (dispatch disabled, score=%.1f, reason=%s)", action.Agent, action.Score, action.Reason)
		return
	}

	// Skip list check: don't dispatch issues that are blacklisted.
	issueKey := fmt.Sprintf("%s#%d", repoShortName(action.Repo), action.IssueNum)
	if b.skipList != nil && b.skipList.IsSkipped(issueKey) {
		b.log.Printf("leverage: %s#%d -> SKIPPED (in skip list)", repoShortName(action.Repo), action.IssueNum)
		return
	}

	// Adapter-based dispatch: create a Task and route to Clawta or GH Actions.
	// Dedup: skip if we dispatched this issue recently (10 min cooldown).
	dispatchKey := issueKey
	if last, ok := b.stuckAgentAlerted[dispatchKey]; ok && time.Since(last) < 10*time.Minute {
		return // already dispatched recently
	}

	if len(b.adapters) > 0 && action.Repo != "" {
		taskType := action.TaskType
		if taskType == "" {
			taskType = "code-gen"
		}
		task := &Task{
			ID:       fmt.Sprintf("brain-%d-%d", action.IssueNum, time.Now().Unix()),
			Type:     taskType,
			Repo:     action.Repo,
			Prompt:   fmt.Sprintf("Fix issue #%d: %s", action.IssueNum, action.Reason),
			Priority: "high",
		}
		for _, adapter := range b.adapters {
			if adapter.CanAccept(task) {
				b.log.Printf("leverage: %s#%d -> adapter %s (score=%.1f, reason=%s)",
					action.Repo, action.IssueNum, adapter.Name(), action.Score, action.Reason)

				// Register dedup + label synchronously so the brain
				// immediately moves on. Adapter execution is async.
				b.stuckAgentAlerted[dispatchKey] = time.Now()
				if action.IssueNum > 0 {
					if err := b.addIssueLabel(ctx, action.Repo, action.IssueNum, LabelClaimed); err != nil {
						b.log.Printf("label: failed to add %s to %s#%d: %v", LabelClaimed, action.Repo, action.IssueNum, err)
					} else {
						b.log.Printf("label: %s#%d -> %s", action.Repo, action.IssueNum, LabelClaimed)
					}
				}

				// Fire adapter dispatch in a goroutine so the brain tick
				// is not blocked by long-running adapters (e.g. Clawta ~10 min).
				go func(a Adapter, t *Task, repo string, issueNum int) {
					result, err := a.Dispatch(context.Background(), t)
					if err != nil {
						b.log.Printf("adapter %s async result: %s#%d error: %v", a.Name(), repo, issueNum, err)
						b.notifyAdapterResult(a.Name(), repo, issueNum, "error", err.Error())
						return
					}
					b.log.Printf("adapter %s async result: %s#%d -> %s", a.Name(), repo, issueNum, result.Status)
					b.notifyAdapterResult(a.Name(), repo, issueNum, result.Status, result.Error)
				}(adapter, task, action.Repo, action.IssueNum)
				return
			}
		}
		b.log.Printf("leverage: no adapter accepted task for %s#%d", action.Repo, action.IssueNum)
	}

	// Fallback: legacy agent-name queue dispatch
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

// notifyAdapterResult posts a Slack notification for an adapter dispatch outcome.
func (b *Brain) notifyAdapterResult(adapter, repo string, issueNum int, status, errMsg string) {
	if b.notifier == nil || !b.notifier.Enabled() {
		return
	}
	if err := b.notifier.PostAdapterDispatch(context.Background(), adapter, repo, issueNum, status, errMsg); err != nil {
		b.log.Printf("slack adapter dispatch: %v", err)
	}
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
		"clawta":     "clawta-sr",
		"sentinel":   "sentinel-sr",
		"llmint":     "llmint-sr",
		"ops":        "kernel-sr",
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

		if os.Getenv("OCTI_BRAIN_DISPATCH") == "0" {
			b.log.Printf("recovery: %s -> BLOCKED (dispatch disabled)", agent)
			continue
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

// inferTaskType guesses the adapter task type from an issue title.
// Returns "bugfix" for bug-related titles, "config" for config/CI titles,
// and "code-gen" as the default.
func inferTaskType(title string) string {
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "bug") || strings.Contains(lower, "fix") || strings.Contains(lower, "broken"):
		return "bugfix"
	case strings.Contains(lower, "config") || strings.Contains(lower, "ci") || strings.Contains(lower, "yaml"):
		return "config"
	default:
		return "code-gen"
	}
}

// repoShortName extracts the repo name from "owner/repo" format.
// "chitinhq/octi" → "octi", "octi" → "octi"
func repoShortName(repo string) string {
	if idx := strings.LastIndex(repo, "/"); idx >= 0 {
		return repo[idx+1:]
	}
	return repo
}
