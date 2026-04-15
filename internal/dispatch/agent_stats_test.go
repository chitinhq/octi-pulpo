package dispatch

import (
	"testing"
)

func TestAgentStats_RecordDispatchAndSuccess(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	for i := 0; i < 3; i++ {
		if err := ps.RecordDispatch(ctx, "agent-a"); err != nil {
			t.Fatalf("record dispatch: %v", err)
		}
	}
	if err := ps.RecordSuccess(ctx, "agent-a", "https://example/pr/1"); err != nil {
		t.Fatalf("record success: %v", err)
	}

	s, err := ps.GetStats(ctx, "agent-a")
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if s.DispatchesTotal != 3 {
		t.Fatalf("want 3 dispatches, got %d", s.DispatchesTotal)
	}
	if s.SuccessesTotal != 1 {
		t.Fatalf("want 1 success, got %d", s.SuccessesTotal)
	}
	if s.LastPRURL != "https://example/pr/1" {
		t.Fatalf("want pr url, got %q", s.LastPRURL)
	}
	if s.LastPRMergedAt == "" {
		t.Fatalf("want timestamp, got empty")
	}
	if rate := s.SuccessRate(); rate < 0.33 || rate > 0.34 {
		t.Fatalf("want success rate ~0.333, got %.3f", rate)
	}
}

func TestLeaderboard_IncludesStatsOnlyAgents(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// Three agents, dispatch-only (no RecordRun completion callbacks).
	// agent-high: 10 dispatches, 5 successes -> score 25.0 + 1.0 = 26.0
	// agent-mid:  5 dispatches,  1 success   -> score 5.0 + 0.5 = 5.5
	// agent-low:  2 dispatches,  0 successes -> score 0 + 0.2 = 0.2
	for i := 0; i < 10; i++ {
		ps.RecordDispatch(ctx, "agent-high")
	}
	for i := 0; i < 5; i++ {
		ps.RecordSuccess(ctx, "agent-high", "")
	}
	for i := 0; i < 5; i++ {
		ps.RecordDispatch(ctx, "agent-mid")
	}
	ps.RecordSuccess(ctx, "agent-mid", "")
	for i := 0; i < 2; i++ {
		ps.RecordDispatch(ctx, "agent-low")
	}

	entries, err := ps.Leaderboard(ctx)
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Agent != "agent-high" {
		t.Fatalf("want agent-high first, got %s", entries[0].Agent)
	}
	if entries[1].Agent != "agent-mid" {
		t.Fatalf("want agent-mid second, got %s", entries[1].Agent)
	}
	if entries[2].Agent != "agent-low" {
		t.Fatalf("want agent-low third, got %s", entries[2].Agent)
	}
	if entries[0].Rank != 1 || entries[2].Rank != 3 {
		t.Fatalf("ranks not assigned: %+v", entries)
	}
}
