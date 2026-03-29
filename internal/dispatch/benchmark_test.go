package dispatch

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBenchmarkTracker_Compute_Empty(t *testing.T) {
	d, ctx := testSetup(t)
	bt := NewBenchmarkTracker(d.rdb, d.namespace)

	m, err := bt.Compute(ctx)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if m.ActiveAgents != 0 {
		t.Fatalf("expected 0 active agents, got %d", m.ActiveAgents)
	}
}

func TestBenchmarkTracker_Compute_WithResults(t *testing.T) {
	d, ctx := testSetup(t)
	bt := NewBenchmarkTracker(d.rdb, d.namespace)

	// Seed worker results
	now := time.Now().UTC()
	results := []workerResult{
		{Agent: "kernel-sr", ExitCode: 0, DurationSec: 120, Timestamp: now.Format(time.RFC3339)},
		{Agent: "cloud-sr", ExitCode: 0, DurationSec: 90, Timestamp: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{Agent: "idle-agent", ExitCode: 0, DurationSec: 5, Timestamp: now.Add(-10 * time.Minute).Format(time.RFC3339)},
		{Agent: "fail-agent", ExitCode: 1, DurationSec: 30, Timestamp: now.Add(-15 * time.Minute).Format(time.RFC3339)},
	}

	key := d.namespace + ":worker-results"
	for _, r := range results {
		data, _ := json.Marshal(r)
		d.rdb.LPush(ctx, key, data)
	}

	m, err := bt.Compute(ctx)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}

	// 3 out of 4 passed
	if m.PassRate < 0.7 || m.PassRate > 0.8 {
		t.Fatalf("expected ~75%% pass rate, got %.2f", m.PassRate)
	}

	// 1 out of 4 was waste (<10s)
	if m.WastePercent < 20 || m.WastePercent > 30 {
		t.Fatalf("expected ~25%% waste, got %.1f%%", m.WastePercent)
	}

	if m.ActiveAgents != 4 {
		t.Fatalf("expected 4 active agents, got %d", m.ActiveAgents)
	}
}

func TestContainsAny(t *testing.T) {
	if !containsAny("pr-merger-agent", "pr-merger") {
		t.Fatal("expected match for pr-merger")
	}
	if !containsAny("workspace-pr-review-agent", "pr-review") {
		t.Fatal("expected match for pr-review")
	}
	if containsAny("kernel-sr", "pr-merger", "reviewer") {
		t.Fatal("expected no match for kernel-sr")
	}
}
