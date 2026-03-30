package dispatch

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBrain_Tick_EmptyQueue(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	// Tick on empty queue should not panic or error
	brain.Tick(ctx)

	// Verify queue is still empty
	depth, err := d.PendingCount(ctx)
	if err != nil {
		t.Fatalf("pending count: %v", err)
	}
	if depth != 0 {
		t.Fatalf("expected empty queue, got %d", depth)
	}
}

func TestBrain_Stats(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	stats := brain.Stats(ctx)
	if stats["chain_count"] != len(DefaultChains()) {
		t.Fatalf("chain_count mismatch: %v", stats["chain_count"])
	}
	if stats["tick_interval"] != "1m0s" {
		t.Fatalf("tick_interval mismatch: %v", stats["tick_interval"])
	}
}

func TestBrain_SetTickInterval(t *testing.T) {
	d, _ := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	brain.SetTickInterval(30 * time.Second)
	if brain.tickInterval != 30*time.Second {
		t.Fatalf("expected 30s, got %s", brain.tickInterval)
	}
}

func TestFormatChainGraph(t *testing.T) {
	chains := ChainConfig{
		"test-sr": {
			OnCommit:  []string{"test-qa"},
			OnFailure: []string{"test-triage"},
		},
	}

	graph := FormatChainGraph(chains)
	if graph == "" {
		t.Fatal("expected non-empty graph")
	}

	// Should contain the agent name and arrows
	if !containsString(graph, "test-sr") {
		t.Fatal("expected test-sr in graph")
	}
	if !containsString(graph, "test-qa") {
		t.Fatal("expected test-qa in graph")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestBrain_CheckStuckAgents verifies that a triaged agent appears in the
// stuckAgentAlerted map after checkStuckAgents runs, and is not re-alerted
// within the 12h dedup window.
func TestBrain_CheckStuckAgents(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	// Wire a disabled notifier (no webhook) so Post* calls are no-ops.
	brain.SetNotifier(NewNotifier(""))

	ps := NewProfileStore(d.rdb, d.namespace, func(string) time.Duration { return 3 * time.Minute })
	brain.SetProfileStore(ps)

	// Record 3 consecutive failures to set TriageFlag.
	for i := 0; i < 3; i++ {
		if err := ps.RecordRun(ctx, "kernel-sr", RunResult{ExitCode: 1, Duration: 5}); err != nil {
			t.Fatalf("record run: %v", err)
		}
	}

	profile, _ := ps.GetProfile(ctx, "kernel-sr")
	if !profile.TriageFlag {
		t.Fatal("expected TriageFlag=true after 3 failures")
	}

	// First self-heal: should populate stuckAgentAlerted.
	brain.checkStuckAgents(ctx)
	if _, ok := brain.stuckAgentAlerted["kernel-sr"]; !ok {
		t.Fatal("expected kernel-sr in stuckAgentAlerted after first check")
	}

	// Record the alerted time, then run again immediately.
	firstAlert := brain.stuckAgentAlerted["kernel-sr"]
	brain.checkStuckAgents(ctx)
	if brain.stuckAgentAlerted["kernel-sr"] != firstAlert {
		t.Fatal("expected dedup: no re-alert within 12h window")
	}
}

// TestBrain_CheckStuckAgents_ClearsOnRecovery verifies that once a TriageFlag
// is cleared (agent succeeded), the stuck-agent alert is no longer fired.
func TestBrain_CheckStuckAgents_ClearsOnRecovery(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())
	brain.SetNotifier(NewNotifier(""))

	ps := NewProfileStore(d.rdb, d.namespace, func(string) time.Duration { return 3 * time.Minute })
	brain.SetProfileStore(ps)

	// 3 failures → triage flag
	for i := 0; i < 3; i++ {
		ps.RecordRun(ctx, "cloud-sr", RunResult{ExitCode: 1, Duration: 5})
	}

	// Successful run clears triage flag
	ps.RecordRun(ctx, "cloud-sr", RunResult{ExitCode: 0, Duration: 60, HadCommits: true})

	profile, _ := ps.GetProfile(ctx, "cloud-sr")
	if profile.TriageFlag {
		t.Fatal("expected TriageFlag=false after successful run")
	}

	// Self-heal should not mark cloud-sr
	brain.checkStuckAgents(ctx)
	if _, ok := brain.stuckAgentAlerted["cloud-sr"]; ok {
		t.Fatal("unexpected alert: cloud-sr TriageFlag cleared but still appeared in stuckAgentAlerted")
	}
}

// TestBrain_CheckInactiveSquads verifies that squads with recent activity are
// not flagged, and that squads with no dispatch log entries are ignored.
func TestBrain_CheckInactiveSquads(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())
	brain.SetNotifier(NewNotifier(""))

	ps := NewProfileStore(d.rdb, d.namespace, func(string) time.Duration { return 3 * time.Minute })
	brain.SetProfileStore(ps)

	// Empty dispatch log — no squads to alert.
	brain.checkInactiveSquads(ctx)
	if len(brain.inactiveSquadAlerted) != 0 {
		t.Fatalf("expected no alerts on empty log, got %d", len(brain.inactiveSquadAlerted))
	}
}

// TestBrain_CheckInactiveSquads_ActiveSquad verifies that a squad with recent
// activity (< 24h) is NOT flagged as inactive.
func TestBrain_CheckInactiveSquads_ActiveSquad(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())
	brain.SetNotifier(NewNotifier(""))

	ps := NewProfileStore(d.rdb, d.namespace, func(string) time.Duration { return 3 * time.Minute })
	brain.SetProfileStore(ps)

	// Write a recent dispatch record for kernel-sr (< 24h ago).
	recentRecord := DispatchRecord{
		Agent:     "kernel-sr",
		Result:    "dispatched",
		Timestamp: time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
	}
	data := mustMarshal(t, recentRecord)
	d.rdb.LPush(ctx, d.namespace+":dispatch-log", data)

	brain.checkInactiveSquads(ctx)
	if _, ok := brain.inactiveSquadAlerted["kernel"]; ok {
		t.Fatal("kernel squad should NOT be flagged: activity within 24h")
	}
}

// TestBrain_SelfHeal_NoProfileStore verifies that maybeSelfHeal is a no-op
// when no ProfileStore is set.
func TestBrain_SelfHeal_NoProfileStore(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	// No profiles set — maybeSelfHeal should return without panicking.
	brain.maybeSelfHeal(ctx)

	if len(brain.stuckAgentAlerted) != 0 || len(brain.inactiveSquadAlerted) != 0 {
		t.Fatal("expected empty alert maps when no ProfileStore configured")
	}
}

// mustMarshal is a test helper that marshals v to JSON or fails the test.
func mustMarshal(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
