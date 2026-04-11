package dispatch

import (
	"testing"
	"time"
)

func TestStagger_AlternatesPlatforms(t *testing.T) {
	s := NewStaggerTracker(nil, "test")
	platform := s.NextPlatform(true, true)
	if platform != "copilot" && platform != "claude" {
		t.Fatalf("NextPlatform() = %q, want copilot or claude", platform)
	}
	s.RecordDispatch(platform, time.Now())
	next := s.NextPlatform(true, true)
	if next == platform {
		t.Errorf("NextPlatform() = %q again, should alternate", next)
	}
}

func TestStagger_SkipsUnavailablePlatform(t *testing.T) {
	s := NewStaggerTracker(nil, "test")
	platform := s.NextPlatform(false, true)
	if platform != "claude" {
		t.Errorf("NextPlatform(copilot=false) = %q, want claude", platform)
	}
	platform = s.NextPlatform(true, false)
	if platform != "copilot" {
		t.Errorf("NextPlatform(claude=false) = %q, want copilot", platform)
	}
	platform = s.NextPlatform(false, false)
	if platform != "" {
		t.Errorf("NextPlatform(none) = %q, want empty", platform)
	}
}

func TestStagger_RespectsMinCooldown(t *testing.T) {
	s := NewStaggerTracker(nil, "test")
	s.CopilotCooldown = 30 * time.Minute
	s.ClaudeCooldown = 45 * time.Minute
	s.RecordDispatch("copilot", time.Now())
	if s.IsAvailable("copilot", time.Now()) {
		t.Error("copilot should be on cooldown")
	}
	if !s.IsAvailable("copilot", time.Now().Add(31*time.Minute)) {
		t.Error("copilot should be available after 31 min")
	}
}

func TestStagger_DailyCaps(t *testing.T) {
	s := NewStaggerTracker(nil, "test")
	s.CopilotDailyCap = 2
	s.ClaudeDailyCap = 2
	now := time.Now()
	s.RecordDispatch("copilot", now)
	s.RecordDispatch("copilot", now.Add(time.Hour))
	if s.IsUnderDailyCap("copilot", now) {
		t.Error("copilot should be at daily cap")
	}
	if !s.IsUnderDailyCap("claude", now) {
		t.Error("claude should be under cap")
	}
}

func TestStagger_NPlatforms(t *testing.T) {
	s := NewStaggerTracker(nil, "test")
	s.RegisterPlatform("claude", 45*time.Minute, 6)
	s.RegisterPlatform("copilot", 30*time.Minute, 8)
	s.RegisterPlatform("gemini", 20*time.Minute, 10)
	s.RegisterPlatform("codex", 15*time.Minute, 5)

	for _, p := range []string{"claude", "copilot", "gemini", "codex"} {
		if !s.IsAvailable(p, time.Now()) {
			t.Errorf("expected %s available initially", p)
		}
	}

	priority := []string{"claude", "copilot", "gemini", "codex"}
	avail := map[string]bool{"claude": false, "copilot": false, "gemini": true, "codex": true}
	got := s.NextPlatformFromList(priority, avail)
	if got != "gemini" {
		t.Errorf("expected gemini (first available), got %s", got)
	}
}
