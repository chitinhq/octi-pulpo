package dispatch

import (
	"testing"
	"time"
)

func TestProfileStore_RecordAndGet(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// Record a productive run
	err := ps.RecordRun(ctx, "test-sr", RunResult{
		ExitCode:   0,
		Duration:   120.5,
		HadCommits: true,
		Timestamp:  "2026-03-29T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("record run: %v", err)
	}

	profile, err := ps.GetProfile(ctx, "test-sr")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}

	if len(profile.RecentResults) != 1 {
		t.Fatalf("expected 1 result, got %d", len(profile.RecentResults))
	}
	if profile.AvgDuration != 120.5 {
		t.Fatalf("expected avg duration 120.5, got %.1f", profile.AvgDuration)
	}
	if profile.FailRate != 0 {
		t.Fatalf("expected 0 fail rate, got %.2f", profile.FailRate)
	}
}

func TestProfileStore_KeepsLast10(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	for i := 0; i < 15; i++ {
		ps.RecordRun(ctx, "prolific-agent", RunResult{
			ExitCode: 0,
			Duration: float64(i * 10),
		})
	}

	profile, _ := ps.GetProfile(ctx, "prolific-agent")
	if len(profile.RecentResults) != 10 {
		t.Fatalf("expected 10 results (capped), got %d", len(profile.RecentResults))
	}

	// First result should be the 6th run (index 5), not the first
	if profile.RecentResults[0].Duration != 50 {
		t.Fatalf("expected first result duration=50, got %.0f", profile.RecentResults[0].Duration)
	}
}

func TestProfileStore_ConsecutiveIdles(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// Record 3 idle runs
	for i := 0; i < 3; i++ {
		ps.RecordRun(ctx, "idle-agent", RunResult{
			ExitCode:   0,
			Duration:   5.0,
			HadCommits: false,
		})
	}

	profile, _ := ps.GetProfile(ctx, "idle-agent")
	if profile.ConsecutiveIdles != 3 {
		t.Fatalf("expected 3 consecutive idles, got %d", profile.ConsecutiveIdles)
	}

	// Record a productive run — resets counter
	ps.RecordRun(ctx, "idle-agent", RunResult{
		ExitCode:   0,
		Duration:   60.0,
		HadCommits: true,
	})

	profile, _ = ps.GetProfile(ctx, "idle-agent")
	if profile.ConsecutiveIdles != 0 {
		t.Fatalf("expected 0 consecutive idles after productive run, got %d", profile.ConsecutiveIdles)
	}
}

func TestAdaptiveCooldown_Productive(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	ps.RecordRun(ctx, "productive-agent", RunResult{
		ExitCode:   0,
		Duration:   120.0,
		HadCommits: true,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "productive-agent")
	if cooldown != 5*time.Minute {
		t.Fatalf("expected 5m for productive agent, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_Idle(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 10 * time.Minute }
	ps := NewProfileStore(d.rdb, d.namespace, static)

	ps.RecordRun(ctx, "idle-agent", RunResult{
		ExitCode:   0,
		Duration:   5.0,
		HadCommits: false,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "idle-agent")
	// Should double from 10m -> 20m
	if cooldown != 20*time.Minute {
		t.Fatalf("expected 20m for idle agent, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_Failing(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 15 * time.Minute }
	ps := NewProfileStore(d.rdb, d.namespace, static)

	// Record mostly failures
	for i := 0; i < 4; i++ {
		ps.RecordRun(ctx, "fail-agent", RunResult{
			ExitCode: 1,
			Duration: 30.0,
		})
	}
	ps.RecordRun(ctx, "fail-agent", RunResult{
		ExitCode: 0,
		Duration: 30.0,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "fail-agent")
	// 80% fail rate > 50%, should double from 15m -> 30m
	if cooldown != 30*time.Minute {
		t.Fatalf("expected 30m for failing agent, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_NoHistory(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// kernel-sr has a 3h static cooldown
	cooldown := ps.AdaptiveCooldown(ctx, "kernel-sr")
	if cooldown != 3*time.Hour {
		t.Fatalf("expected 3h static fallback for kernel-sr, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_IdleMaxCap(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 4 * time.Hour }
	ps := NewProfileStore(d.rdb, d.namespace, static)

	ps.RecordRun(ctx, "very-idle", RunResult{
		ExitCode:   0,
		Duration:   2.0,
		HadCommits: false,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "very-idle")
	// Doubling 4h would be 8h, but cap is 6h
	if cooldown != 6*time.Hour {
		t.Fatalf("expected 6h cap for idle agent, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_FailMaxCap(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 90 * time.Minute }
	ps := NewProfileStore(d.rdb, d.namespace, static)

	for i := 0; i < 5; i++ {
		ps.RecordRun(ctx, "fail-cap-agent", RunResult{
			ExitCode: 1,
			Duration: 30.0,
		})
	}

	cooldown := ps.AdaptiveCooldown(ctx, "fail-cap-agent")
	// Doubling 90m would be 180m = 3h, but fail cap is 2h
	if cooldown != 2*time.Hour {
		t.Fatalf("expected 2h cap for failing agent, got %s", cooldown)
	}
}

// testAdaptiveCooldownForDispatch verifies that the dispatcher integration point works.
func TestAdaptiveCooldown_DefaultFallback(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, func(string) time.Duration { return 0 })

	// Agent with no static cooldown and no history
	cooldown := ps.AdaptiveCooldown(ctx, "unknown-agent")
	// Should get static fallback (which is 0 here), so the function returns 0
	if cooldown != 0 {
		t.Fatalf("expected 0 for unknown agent with no static, got %s", cooldown)
	}
}
