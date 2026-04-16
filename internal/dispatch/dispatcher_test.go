package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/coordination"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/redis/go-redis/v9"
)

// testSetup creates a Dispatcher backed by real Redis for integration tests.
// Requires Redis on localhost:6379 (the standard dev setup).
func testSetup(t *testing.T) (*Dispatcher, context.Context) {
	t.Helper()

	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}

	// Use a unique namespace per test to avoid cross-contamination
	ns := "octi-test-" + t.Name()

	// Clean up test keys before and after
	cleanup := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	cleanup()
	t.Cleanup(func() {
		cleanup()
		rdb.Close()
	})

	// Create a health directory with a healthy driver.
	// Use NewRouterWithTiers so only the explicitly registered drivers are
	// candidates — prevents global driverTiers entries from leaking in.
	healthDir := t.TempDir()
	writeHealthFile(t, healthDir, "claude-code", "CLOSED")

	coord, err := coordination.New(redisURL, ns)
	if err != nil {
		t.Fatalf("coordination engine: %v", err)
	}
	t.Cleanup(func() { coord.Close() })

	router := routing.NewRouterWithTiers(healthDir, map[string]routing.CostTier{"claude-code": routing.TierCLI})
	eventRouter := NewEventRouter(DefaultRules())

	queueFile := filepath.Join(t.TempDir(), "queue.txt")

	d := NewDispatcher(rdb, router, coord, eventRouter, queueFile, ns)
	return d, ctx
}

func writeHealthFile(t *testing.T, dir, driver, state string) {
	t.Helper()
	hf := map[string]interface{}{"state": state, "failures": 0}
	data, _ := json.Marshal(hf)
	if err := os.WriteFile(filepath.Join(dir, driver+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// --- Dispatch Tests ---

func TestDispatch_BasicSuccess(t *testing.T) {
	d, ctx := testSetup(t)

	event := Event{Type: EventManual, Source: "test"}
	result, err := d.Dispatch(ctx, event, "test-agent", 2)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if result.Action != "dispatched" {
		t.Fatalf("expected action=dispatched, got %s (reason: %s)", result.Action, result.Reason)
	}
	if result.Driver == "" {
		t.Fatal("expected a driver to be assigned")
	}
	if result.ClaimID == "" {
		t.Fatal("expected a claim ID")
	}
}

func TestDispatch_RespectsCoordClaim(t *testing.T) {
	d, ctx := testSetup(t)

	// First dispatch succeeds and creates a claim
	event := Event{Type: EventManual, Source: "test"}
	result1, err := d.Dispatch(ctx, event, "agent-dedup", 2)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if result1.Action != "dispatched" {
		t.Fatalf("expected first dispatch to succeed, got %s", result1.Action)
	}

	// Second dispatch should be skipped (agent already has a claim)
	result2, err := d.Dispatch(ctx, event, "agent-dedup", 2)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if result2.Action != "skipped" {
		t.Fatalf("expected second dispatch to be skipped, got %s (reason: %s)", result2.Action, result2.Reason)
	}
	if result2.Reason == "" {
		t.Fatal("expected a reason for skip")
	}
}

func TestDispatch_CooldownPreventsRapidRedispatch(t *testing.T) {
	d, ctx := testSetup(t)

	// Set a short cooldown for this test agent
	d.SetCooldown(ctx, "cooldown-agent", 5*time.Second)

	event := Event{Type: EventManual, Source: "test"}
	result, err := d.Dispatch(ctx, event, "cooldown-agent", 2)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if result.Action != "skipped" {
		t.Fatalf("expected skipped due to cooldown, got %s", result.Action)
	}
	if result.Reason == "" || result.Reason == "agent already has active claim (another instance running)" {
		t.Fatalf("expected cooldown reason, got: %s", result.Reason)
	}
}

func TestDispatch_DriversExhausted_QueuesForLater(t *testing.T) {
	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}

	ns := "octi-test-exhausted"
	keys, _ := rdb.Keys(ctx, ns+":*").Result()
	if len(keys) > 0 {
		rdb.Del(ctx, keys...)
	}
	t.Cleanup(func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})

	// Health directory with ALL drivers OPEN
	healthDir := t.TempDir()
	writeHealthFile(t, healthDir, "claude-code", "OPEN")
	writeHealthFile(t, healthDir, "copilot", "OPEN")

	coord, _ := coordination.New(redisURL, ns)
	t.Cleanup(func() { coord.Close() })

	router := routing.NewRouterWithTiers(healthDir, map[string]routing.CostTier{
		"claude-code": routing.TierCLI,
		"copilot":     routing.TierCLI,
	})
	eventRouter := NewEventRouter(DefaultRules())
	d := NewDispatcher(rdb, router, coord, eventRouter, "", ns)

	event := Event{Type: EventManual, Source: "test"}
	result, err := d.Dispatch(ctx, event, "queued-agent", 2)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if result.Action != "queued" {
		t.Fatalf("expected action=queued when all drivers OPEN, got %s (reason: %s)", result.Action, result.Reason)
	}
	if result.QueuePos < 1 {
		t.Fatalf("expected queue position >= 1, got %d", result.QueuePos)
	}
}

// --- Priority Queue Tests ---

func TestPriorityQueue_Ordering(t *testing.T) {
	d, ctx := testSetup(t)

	// Enqueue agents with different priorities
	d.Enqueue(ctx, "background-agent", 3)
	d.Enqueue(ctx, "critical-agent", 0)
	d.Enqueue(ctx, "normal-agent", 2)
	d.Enqueue(ctx, "high-agent", 1)

	// Dequeue should return in priority order (lowest score first)
	expected := []string{"critical-agent", "high-agent", "normal-agent", "background-agent"}
	for _, want := range expected {
		got, err := d.Dequeue(ctx)
		if err != nil {
			t.Fatalf("dequeue error: %v", err)
		}
		if got != want {
			t.Fatalf("expected %s, got %s", want, got)
		}
	}

	// Queue should be empty now
	count, _ := d.PendingCount(ctx)
	if count != 0 {
		t.Fatalf("expected empty queue, got %d", count)
	}
}

func TestPriorityQueue_DequeueEmpty(t *testing.T) {
	d, ctx := testSetup(t)

	agent, err := d.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue error: %v", err)
	}
	if agent != "" {
		t.Fatalf("expected empty string from empty queue, got %s", agent)
	}
}

func TestPriorityQueue_PendingCount(t *testing.T) {
	d, ctx := testSetup(t)

	d.Enqueue(ctx, "a", 1)
	d.Enqueue(ctx, "b", 2)
	d.Enqueue(ctx, "c", 3)

	count, err := d.PendingCount(ctx)
	if err != nil {
		t.Fatalf("pending count error: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 pending, got %d", count)
	}
}

// --- Event Routing Tests ---

func TestEventRouter_MatchesPROpened(t *testing.T) {
	er := NewEventRouter(DefaultRules())

	event := Event{
		Type:   EventPROpened,
		Source: "github",
		Repo:   "chitinhq/kernel",
	}

	matches := er.Match(event)
	if len(matches) == 0 {
		t.Fatal("expected at least one match for pr.opened on chitinhq/chitin")
	}

	found := false
	for _, m := range matches {
		if m.AgentName == "workspace-pr-review-agent" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected workspace-pr-review-agent to match")
	}
}

func TestEventRouter_MatchesCICompleted(t *testing.T) {
	er := NewEventRouter(DefaultRules())

	event := Event{
		Type:   EventCICompleted,
		Source: "github",
		Repo:   "chitinhq/cloud",
	}

	matches := er.Match(event)
	found := false
	for _, m := range matches {
		if m.AgentName == "pr-merger-agent-cloud" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected pr-merger-agent-cloud to match ci.completed on chitin-cloud")
	}
}

func TestEventRouter_NoMatchForUnknownRepo(t *testing.T) {
	er := NewEventRouter(DefaultRules())

	event := Event{
		Type:   EventPROpened,
		Source: "github",
		Repo:   "SomeOrg/some-repo",
	}

	matches := er.Match(event)
	if len(matches) != 0 {
		t.Fatalf("expected no matches for unknown repo, got %d", len(matches))
	}
}

func TestEventRouter_TimerMatchesWithoutRepo(t *testing.T) {
	er := NewEventRouter(DefaultRules())

	event := Event{
		Type:   EventTimer,
		Source: "cron",
	}

	matches := er.Match(event)
	if len(matches) == 0 {
		t.Fatal("expected timer events to match agents")
	}

	agentNames := make(map[string]bool)
	for _, m := range matches {
		agentNames[m.AgentName] = true
	}
	if !agentNames["kernel-sr"] {
		t.Fatal("expected kernel-sr in timer matches")
	}
}

func TestEventRouter_CooldownFor(t *testing.T) {
	er := NewEventRouter(DefaultRules())

	cd := er.CooldownFor("pr-merger-agent")
	if cd != 10*time.Minute {
		t.Fatalf("expected 10m cooldown for pr-merger-agent, got %s", cd)
	}

	cd = er.CooldownFor("kernel-sr")
	if cd != 3*time.Hour {
		t.Fatalf("expected 3h cooldown for kernel-sr, got %s", cd)
	}
}

// --- Bridge Tests ---

func TestBridgeToFileQueue(t *testing.T) {
	d, _ := testSetup(t)

	// Override queue file to a temp location
	queueFile := filepath.Join(t.TempDir(), "queue.txt")
	d.queueFile = queueFile

	// Write first agent
	err := d.BridgeToFileQueue("agent-a")
	if err != nil {
		t.Fatalf("bridge error: %v", err)
	}

	// Write second agent
	err = d.BridgeToFileQueue("agent-b")
	if err != nil {
		t.Fatalf("bridge error: %v", err)
	}

	// Read and verify
	data, err := os.ReadFile(queueFile)
	if err != nil {
		t.Fatalf("read queue file: %v", err)
	}
	content := string(data)
	if content != "agent-a\nagent-b\n" {
		t.Fatalf("expected 'agent-a\\nagent-b\\n', got %q", content)
	}
}

func TestBridgeToFileQueue_Dedup(t *testing.T) {
	d, _ := testSetup(t)

	queueFile := filepath.Join(t.TempDir(), "queue.txt")
	d.queueFile = queueFile

	// Write same agent twice
	d.BridgeToFileQueue("agent-dup")
	d.BridgeToFileQueue("agent-dup")

	data, _ := os.ReadFile(queueFile)
	content := string(data)
	if content != "agent-dup\n" {
		t.Fatalf("expected single entry, got %q", content)
	}
}

func TestBridgeToFileQueue_Disabled(t *testing.T) {
	d, _ := testSetup(t)
	d.queueFile = "" // disable bridge

	err := d.BridgeToFileQueue("agent-x")
	if err != nil {
		t.Fatalf("expected no error when bridge disabled, got %v", err)
	}
}

// --- DispatchEvent Integration Tests ---

func TestDispatchEvent_PROpenedRoutes(t *testing.T) {
	d, ctx := testSetup(t)

	event := Event{
		Type:   EventPROpened,
		Source: "github",
		Repo:   "chitinhq/kernel",
	}

	results, err := d.DispatchEvent(ctx, event)
	if err != nil {
		t.Fatalf("dispatch event error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one dispatch result")
	}

	found := false
	for _, r := range results {
		if r.Agent == "workspace-pr-review-agent" {
			found = true
			if r.Action != "dispatched" {
				t.Fatalf("expected dispatched, got %s (reason: %s)", r.Action, r.Reason)
			}
			break
		}
	}
	if !found {
		t.Fatal("expected workspace-pr-review-agent in results")
	}
}

func TestDispatchEvent_NoRulesMatch(t *testing.T) {
	d, ctx := testSetup(t)

	event := Event{
		Type:   EventBudgetChange,
		Source: "system",
	}

	results, err := d.DispatchEvent(ctx, event)
	if err != nil {
		t.Fatalf("dispatch event error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for unmatched event, got %d", len(results))
	}
}

// --- Dispatch Record Tests ---

func TestRecentDispatches_Recorded(t *testing.T) {
	d, ctx := testSetup(t)

	event := Event{Type: EventManual, Source: "test"}
	d.Dispatch(ctx, event, "recorded-agent", 1)

	records, err := d.RecentDispatches(ctx, 10)
	if err != nil {
		t.Fatalf("recent dispatches error: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected at least one dispatch record")
	}
	if records[0].Agent != "recorded-agent" {
		t.Fatalf("expected recorded-agent, got %s", records[0].Agent)
	}
}

// --- Budget-Aware Dispatch Tests ---

// TestDispatch_BudgetFieldSet verifies that Dispatch() populates result.Budget
// with a non-empty value derived from DynamicBudget().
func TestDispatch_BudgetFieldSet(t *testing.T) {
	d, ctx := testSetup(t)

	event := Event{Type: EventManual, Source: "test"}
	result, err := d.Dispatch(ctx, event, "budget-field-agent", 2)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if result.Budget == "" {
		t.Fatal("expected Budget field to be populated")
	}
	// testSetup writes claude-code as CLOSED (only CLI driver) → DynamicBudget = "medium"
	if result.Budget != "medium" {
		t.Fatalf("expected budget=medium (single healthy CLI driver), got %s", result.Budget)
	}
}

// TestDispatchBudget_ExplicitHighAllowsAPITier verifies that DispatchBudget with
// budget="high" can route to API-tier drivers.
func TestDispatchBudget_ExplicitHighAllowsAPITier(t *testing.T) {
	t.Helper()

	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}

	ns := "octi-test-" + t.Name()
	cleanup := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	cleanup()
	t.Cleanup(func() { cleanup(); rdb.Close() })

	// Set up a health dir where all CLI drivers are OPEN but API driver is healthy.
	healthDir := t.TempDir()
	writeHealthFile(t, healthDir, "claude-code", "OPEN") // CLI exhausted

	tiers := map[string]routing.CostTier{
		"claude-code": routing.TierCLI,
		"claude-api":  routing.TierAPI,
	}
	router := routing.NewRouterWithTiers(healthDir, tiers)
	coord, err := coordination.New(redisURL, ns)
	if err != nil {
		t.Fatalf("coordination engine: %v", err)
	}
	t.Cleanup(func() { coord.Close() })

	d := NewDispatcher(rdb, router, coord, NewEventRouter(DefaultRules()), filepath.Join(t.TempDir(), "q.txt"), ns)

	event := Event{Type: EventManual, Source: "test"}

	// Dynamic budget (all CLI OPEN) → "low", which caps at local only → should queue or skip
	dynResult, err := d.Dispatch(ctx, event, "api-tier-agent", 2)
	if err != nil {
		t.Fatalf("dynamic dispatch error: %v", err)
	}
	// With budget="low", API-tier driver is above the cap — expect queued or skipped
	if dynResult.Action == "dispatched" && dynResult.Driver == "claude-api" {
		t.Fatalf("dynamic dispatch should NOT route to API tier, got driver=%s", dynResult.Driver)
	}

	// Release claim to allow second dispatch attempt
	d.ReleaseClaim(ctx, "api-tier-agent")
	d.ClearCooldown(ctx, "api-tier-agent")

	// Explicit budget="high" → can reach API tier
	highResult, err := d.DispatchBudget(ctx, event, "api-tier-agent", 2, "high")
	if err != nil {
		t.Fatalf("explicit-high dispatch error: %v", err)
	}
	if highResult.Budget != "high" {
		t.Fatalf("expected budget=high in result, got %s", highResult.Budget)
	}
	if highResult.Action == "dispatched" && highResult.Driver != "claude-api" {
		t.Fatalf("expected claude-api (only healthy driver at high budget), got %s", highResult.Driver)
	}
}

// TestDispatchBudget_LowBlocksCLI verifies that budget="low" prevents CLI-tier routing.
func TestDispatchBudget_LowBlocksCLI(t *testing.T) {
	d, ctx := testSetup(t)

	event := Event{Type: EventManual, Source: "test"}
	// testSetup uses only a CLI-tier driver (claude-code); budget="low" caps at TierLocal.
	// The code task requires TierCLI, so minTier > maxTier → Skip.
	result, err := d.DispatchBudget(ctx, event, "low-budget-agent", 2, "low")
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	// With only a CLI driver and budget="low" (local only), dispatch should queue (all drivers
	// exhausted from the perspective of the budget constraint).
	if result.Action == "dispatched" {
		t.Fatalf("expected queued/skipped with budget=low (no local drivers), got dispatched via %s", result.Driver)
	}
	if result.Budget != "low" {
		t.Fatalf("expected budget=low in result, got %s", result.Budget)
	}
}

// TestDispatch_RejectsRepoScopedEventWithEmptyRepo guards the
// silent-loss bug from workspace#408: repo-scoped events (pr.*,
// issue.*, ci.completed, push, brain.leverage) must carry a non-empty
// Repo or adapter CanAccept() rejects the dispatch invisibly. The
// dispatcher must fail loudly instead so the producer can be traced.
func TestDispatch_RejectsRepoScopedEventWithEmptyRepo(t *testing.T) {
	d, ctx := testSetup(t)

	event := Event{
		Type:   EventType("brain.leverage"),
		Source: "brain",
		// Repo intentionally omitted — this is the bug we're guarding.
	}
	result, err := d.Dispatch(ctx, event, "some-agent", 2)
	if err == nil {
		t.Fatalf("expected error for empty Repo on repo-scoped event, got nil (action=%s)", result.Action)
	}
	if result.Action != "skipped" {
		t.Fatalf("expected action=skipped, got %s", result.Action)
	}
	if !strings.Contains(result.Reason, "Repo empty") {
		t.Fatalf("expected reason to mention empty Repo, got %q", result.Reason)
	}
}

// TestDispatch_AcceptsExemptEventWithEmptyRepo confirms system-wide
// events (timer, manual, signal) are still allowed without a Repo.
func TestDispatch_AcceptsExemptEventWithEmptyRepo(t *testing.T) {
	d, ctx := testSetup(t)

	event := Event{Type: EventTimer, Source: "timer"} // no Repo
	result, err := d.Dispatch(ctx, event, "kernel-sr", 2)
	if err != nil {
		t.Fatalf("expected no error for exempt event, got %v", err)
	}
	if result.Action == "skipped" && strings.Contains(result.Reason, "Repo empty") {
		t.Fatalf("exempt event should not be rejected for empty Repo: %s", result.Reason)
	}
}
// --- Silent-loss regression (workspace#408) ---
//
// Before commit 5dc4e27, DispatchBudget set result.Action="dispatched" after
// routing+enqueue without ever invoking the routed adapter. These subtests
// pin the contract: action="dispatched" MUST mean the adapter was called.

type silentLossFakeAdapter struct {
	name      string
	calls     int
	lastTask  *Task
	returnErr error
	returnRes *AdapterResult
}

func (f *silentLossFakeAdapter) Name() string                  { return f.name }
func (f *silentLossFakeAdapter) CanAccept(_ *Task) bool        { return true }
func (f *silentLossFakeAdapter) Dispatch(_ context.Context, task *Task) (*AdapterResult, error) {
	f.calls++
	f.lastTask = task
	return f.returnRes, f.returnErr
}

func TestDispatchBudget_CallsAdapter_SilentLossRegression(t *testing.T) {
	t.Run("happy_path_adapter_invoked", func(t *testing.T) {
		d, ctx := testSetup(t)
		fake := &silentLossFakeAdapter{
			name:      "claude-code",
			returnRes: &AdapterResult{Status: "completed", Adapter: "claude-code"},
		}
		d.SetAdapters(fake)

		event := Event{Type: EventManual, Source: "test", Repo: "chitinhq/octi"}
		result, err := d.DispatchBudget(ctx, event, "happy-agent", 2, "medium")
		if err != nil {
			t.Fatalf("dispatch error: %v", err)
		}
		if result.Action != "dispatched" {
			t.Fatalf("expected action=dispatched, got %s (reason: %s)", result.Action, result.Reason)
		}
		// THE key assertion: without this the buggy parent commit passes.
		if fake.calls != 1 {
			t.Fatalf("silent-loss regression: expected adapter.Dispatch called exactly once, got %d", fake.calls)
		}
		if fake.lastTask == nil || fake.lastTask.Repo != "chitinhq/octi" {
			t.Fatalf("expected task.Repo=chitinhq/octi, got %+v", fake.lastTask)
		}
	})

	t.Run("adapter_error_marks_failed", func(t *testing.T) {
		d, ctx := testSetup(t)
		fake := &silentLossFakeAdapter{
			name:      "claude-code",
			returnErr: fmt.Errorf("boom: surface unreachable"),
		}
		d.SetAdapters(fake)

		event := Event{Type: EventManual, Source: "test", Repo: "chitinhq/octi"}
		result, err := d.DispatchBudget(ctx, event, "err-agent", 2, "medium")
		if err != nil {
			t.Fatalf("dispatch error: %v", err)
		}
		if result.Action != "failed" {
			t.Fatalf("expected action=failed, got %s (reason: %s)", result.Action, result.Reason)
		}
		if fake.calls != 1 {
			t.Fatalf("expected adapter.Dispatch called exactly once, got %d", fake.calls)
		}
		if !strings.Contains(result.Reason, "boom: surface unreachable") {
			t.Fatalf("expected reason to contain adapter error, got: %s", result.Reason)
		}
	})

	t.Run("adapters_registered_but_no_match_unroutable", func(t *testing.T) {
		d, ctx := testSetup(t)
		// Register an adapter whose Name() does NOT match the routed driver
		// (testSetup configures claude-code as the only healthy driver).
		fake := &silentLossFakeAdapter{name: "some-other-driver"}
		d.SetAdapters(fake)

		event := Event{Type: EventManual, Source: "test", Repo: "chitinhq/octi"}
		result, err := d.DispatchBudget(ctx, event, "unroutable-agent", 2, "medium")
		if err != nil {
			t.Fatalf("dispatch error: %v", err)
		}
		if result.Action != "unroutable" {
			t.Fatalf("expected action=unroutable, got %s (reason: %s)", result.Action, result.Reason)
		}
		if fake.calls != 0 {
			t.Fatalf("expected adapter.Dispatch NOT called (no match), got %d calls", fake.calls)
		}
	})
}

// TestDispatchBudget_T1LocalEndToEnd locks in T1-local routing correctness
// end-to-end: agent name without code keywords → router picks the TierLocal
// driver (clawta) → Dispatcher invokes the registered adapter → dispatch-log
// record carries tier="local" and a non-empty dispatch_id join key.
//
// Regression guard for the "local=0" telemetry gap (turing, 2026-04-15): the
// production dispatcher in main.go previously had no adapters registered, so
// even when routing correctly picked clawta, the legacy queue-only fallback
// fired and no real execution surface was invoked. This test is the proof
// that (a) the router picks local when it should, (b) the adapter is called,
// and (c) ClassifyTier emits "local" so swarm_today counts the dispatch.
func TestDispatchBudget_T1LocalEndToEnd(t *testing.T) {
	d, ctx := testSetup(t)

	// Override the router so clawta is the only TierLocal driver and
	// the default gh-actions/anthropic entries don't leak in from the
	// global driverTiers map. Write a healthy file so CircuitState=CLOSED.
	healthDir := t.TempDir()
	writeHealthFile(t, healthDir, "clawta", "CLOSED")
	d.router = routing.NewRouterWithTiers(healthDir, map[string]routing.CostTier{
		"clawta": routing.TierLocal,
	})

	fake := &silentLossFakeAdapter{
		name:      "clawta",
		returnRes: &AdapterResult{Status: "completed", Adapter: "clawta"},
	}
	d.SetAdapters(fake)

	// Agent name deliberately has no code/review/implement keyword so
	// taskMinTier() returns TierLocal. The squad-era "kernel-em" timer
	// was excised in octi#271 Phase 1; use "kernel-sr" which is the
	// surviving live timer agent (see events.go DefaultRules).
	event := Event{Type: EventManual, Source: "test", Repo: "chitinhq/octi"}
	result, err := d.DispatchBudget(ctx, event, "kernel-sr", 2, "medium")
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	// Router should have picked clawta (cheapest local driver, healthy).
	if result.Driver != "clawta" {
		t.Fatalf("expected driver=clawta, got %q (reason: %s)", result.Driver, result.Reason)
	}
	if result.Action != "dispatched" {
		t.Fatalf("expected action=dispatched, got %s (reason: %s)", result.Action, result.Reason)
	}
	// Adapter actually invoked — no silent-loss on the local path.
	if fake.calls != 1 {
		t.Fatalf("expected clawta adapter called once, got %d", fake.calls)
	}
	// dispatch_id correlation key populated (octi#257).
	if result.DispatchID == "" {
		t.Fatal("expected non-empty DispatchID for cross-sink reconcile")
	}
	if fake.lastTask == nil || fake.lastTask.DispatchID != result.DispatchID {
		t.Fatalf("expected task.DispatchID=%q to match result, got task=%+v",
			result.DispatchID, fake.lastTask)
	}

	// Verify the dispatch-log record in Redis carries tier="local" + the
	// join key — this is what swarm_today/tier_activity consume.
	records, err := d.RecentDispatches(ctx, 1)
	if err != nil {
		t.Fatalf("read dispatch log: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 dispatch-log record, got %d", len(records))
	}
	rec := records[0]
	if rec.Tier != "local" {
		t.Fatalf("expected tier=local in dispatch-log, got %q", rec.Tier)
	}
	if rec.Driver != "clawta" {
		t.Fatalf("expected driver=clawta in dispatch-log, got %q", rec.Driver)
	}
	if rec.DispatchID == "" || rec.DispatchID != result.DispatchID {
		t.Fatalf("expected dispatch_id=%q in record, got %q",
			result.DispatchID, rec.DispatchID)
	}
	if rec.Result != "dispatched" {
		t.Fatalf("expected result=dispatched in record, got %q", rec.Result)
	}
}

