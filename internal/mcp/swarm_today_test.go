package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/chitinhq/octi-pulpo/internal/routing"
)

func fixedNow() time.Time {
	return time.Date(2026, 4, 14, 23, 45, 0, 0, time.UTC)
}

func zeroPR(_ context.Context, _ time.Time) (int, int, int, error)           { return 0, 0, 0, nil }
func zeroIssue(_ context.Context, _ time.Time) (int, int, int, []int, error) { return 0, 0, 0, nil, nil }
func zeroRun(_ context.Context, _ time.Time) (time.Time, string, int, int, error) {
	return time.Time{}, "", 0, 0, nil
}

func TestSwarmToday_FullyIdle(t *testing.T) {
	in := swarmTodayInputs{now: fixedNow(), prSearch: zeroPR, issueSearch: zeroIssue, runSearch: zeroRun}
	r := buildSwarmToday(context.Background(), 24, in)
	if r.PRs.Opened != 0 || r.Issues.Filed != 0 {
		t.Fatalf("expected fully idle, got %+v", r)
	}
	if r.Alerts.SilentDispatches != 0 || r.Alerts.StuckAgents != 0 || r.Alerts.StaleDrivers != 0 {
		t.Fatalf("expected zero alerts, got %+v", r.Alerts)
	}
	if !strings.Contains(r.Text, "=== swarm today") {
		t.Fatalf("text missing header: %q", r.Text)
	}
	if !strings.Contains(r.Text, "no swarm-worker data") {
		t.Fatalf("expected dry-swarm note, got %q", r.Text)
	}
	if strings.Contains(r.Text, "vs 7d avg") {
		t.Fatalf("should omit delta when |delta|<1, got %q", r.Text)
	}
}

func TestSwarmToday_Active(t *testing.T) {
	now := fixedNow()
	pr := func(_ context.Context, _ time.Time) (int, int, int, error) { return 3, 2, 1, nil }
	iss := func(_ context.Context, _ time.Time) (int, int, int, []int, error) { return 12, 8, 0, nil, nil }
	run := func(_ context.Context, _ time.Time) (time.Time, string, int, int, error) {
		return now.Add(-15 * time.Minute), "chitin-swarm-worker", 1, 0, nil
	}
	lastSuccess := now.Add(-1 * time.Hour).Format(time.RFC3339)
	in := swarmTodayInputs{
		now:         now,
		pr7dAvg:     4,
		prSearch:    pr,
		issueSearch: iss,
		runSearch:   run,
		drivers: []routing.DriverHealth{
			{Name: "claude-code", CircuitState: "CLOSED", LastSuccess: lastSuccess},
			{Name: "copilot", CircuitState: "CLOSED", LastSuccess: lastSuccess},
		},
		recent: []dispatch.DispatchRecord{
			{Agent: "kernel-01", Driver: "anthropic", Result: "dispatched", Timestamp: now.Add(-10 * time.Minute).Format(time.RFC3339)},
			{Agent: "kernel-02", Driver: "gh-actions", Result: "dispatched", Timestamp: now.Add(-20 * time.Minute).Format(time.RFC3339)},
		},
		budgetTodayC: 47,
		budgetMonthC: 1280,
		budgetCapC:   5000,
	}
	r := buildSwarmToday(context.Background(), 24, in)
	if r.PRs.Opened != 3 || r.PRs.Merged != 2 || r.PRs.InReview != 1 {
		t.Fatalf("prs mismatch: %+v", r.PRs)
	}
	if r.PRs.DeltaVs7dAvg != 1 {
		t.Fatalf("expected delta=+1, got %d", r.PRs.DeltaVs7dAvg)
	}
	if !strings.Contains(r.Text, "(+1 vs 7d avg)") {
		t.Fatalf("text missing delta annotation: %q", r.Text)
	}
	if r.Drivers.CircuitClosed != 2 || r.Drivers.StaleGt48h != 0 {
		t.Fatalf("drivers mismatch: %+v", r.Drivers)
	}
	if r.Swarm.LastRunWorkflow != "chitin-swarm-worker" || r.Swarm.RunsToday != 1 {
		t.Fatalf("swarm mismatch: %+v", r.Swarm)
	}
	if r.Tiers["cloud"].Dispatches != 1 {
		t.Fatalf("expected 1 cloud dispatch, got %+v", r.Tiers)
	}
	if r.Tiers["actions"].Dispatches != 1 {
		t.Fatalf("expected 1 actions dispatch, got %+v", r.Tiers)
	}
	if r.Budget.MonthUSD != 12.80 || r.Budget.MonthCapUSD != 50.00 {
		t.Fatalf("budget mismatch: %+v", r.Budget)
	}
}

func TestSwarmToday_StuckAndSilentLoss(t *testing.T) {
	now := fixedNow()
	pr := func(_ context.Context, _ time.Time) (int, int, int, error) { return 1, 0, 0, nil }
	iss := func(_ context.Context, _ time.Time) (int, int, int, []int, error) {
		return 5, 3, 1, []int{381}, nil
	}
	run := func(_ context.Context, _ time.Time) (time.Time, string, int, int, error) {
		return time.Time{}, "", 0, 0, nil
	}
	var recent []dispatch.DispatchRecord
	for i := 0; i < 5; i++ {
		recent = append(recent, dispatch.DispatchRecord{
			Agent: "foo", Driver: "gh-actions", Result: "rate_limited",
			Timestamp: now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
	}
	stale := now.Add(-72 * time.Hour).Format(time.RFC3339)
	in := swarmTodayInputs{
		now: now, prSearch: pr, issueSearch: iss, runSearch: run, recent: recent,
		drivers: []routing.DriverHealth{{Name: "codex", CircuitState: "OPEN", LastSuccess: stale}},
	}
	r := buildSwarmToday(context.Background(), 24, in)
	if r.Issues.Stuck != 1 || len(r.Issues.StuckRefs) != 1 || r.Issues.StuckRefs[0] != 381 {
		t.Fatalf("stuck issues: %+v", r.Issues)
	}
	if !strings.Contains(r.Text, "#381") {
		t.Fatalf("text should link stuck ref: %q", r.Text)
	}
	tc := r.Tiers["actions"]
	if !tc.SilentLoss || tc.Dispatches != 5 || tc.Completions != 0 {
		t.Fatalf("expected silent-loss on actions tier, got %+v", tc)
	}
	if !strings.Contains(r.Text, "silent-loss suspected") {
		t.Fatalf("text missing silent-loss note: %q", r.Text)
	}
	if r.Drivers.StaleGt48h != 1 || len(r.Drivers.StaleNames) != 1 || r.Drivers.StaleNames[0] != "codex" {
		t.Fatalf("stale drivers: %+v", r.Drivers)
	}
	if !strings.Contains(r.Text, "codex") {
		t.Fatalf("text should name stale driver: %q", r.Text)
	}
	if r.Alerts.SilentDispatches != 5 || r.Alerts.StuckAgents != 1 || r.Alerts.StaleDrivers != 1 {
		t.Fatalf("alerts mismatch: %+v", r.Alerts)
	}
}

func TestSwarmToday_TextShape(t *testing.T) {
	in := swarmTodayInputs{now: fixedNow(), prSearch: zeroPR, issueSearch: zeroIssue, runSearch: zeroRun}
	r := buildSwarmToday(context.Background(), 24, in)
	lines := strings.Split(strings.TrimRight(r.Text, "\n"), "\n")
	if len(lines) > 25 {
		t.Fatalf("text exceeds 25 lines (%d): %q", len(lines), r.Text)
	}
	for i, l := range lines {
		if len(l) > 100 {
			t.Fatalf("line %d >100 cols (%d): %q", i, len(l), l)
		}
	}
}
