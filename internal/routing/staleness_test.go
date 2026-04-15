package routing

import (
	"strings"
	"testing"
	"time"
)

func TestRecommendAction_Healthy(t *testing.T) {
	h := DriverHealth{
		CircuitState:         "CLOSED",
		LastSuccess:          time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
		DaysSinceLastSuccess: 0,
	}
	if got := RecommendAction(h); got != "healthy" {
		t.Fatalf("want healthy, got %q", got)
	}
}

// Canonical decommissioned-driver bug: CLOSED circuit, 5d stale.
func TestRecommendAction_StaleClosed(t *testing.T) {
	h := DriverHealth{
		CircuitState:         "CLOSED",
		LastSuccess:          time.Now().UTC().Add(-5 * 24 * time.Hour).Format(time.RFC3339),
		DaysSinceLastSuccess: 5,
	}
	got := RecommendAction(h)
	if !strings.Contains(got, "stale") || !strings.Contains(got, "5d") {
		t.Fatalf("expected stale+5d, got %q", got)
	}
}

// New driver (empty LastSuccess) not library-stale; MCP layer judges.
func TestRecommendAction_NewDriverNotStale(t *testing.T) {
	h := DriverHealth{CircuitState: "CLOSED", LastSuccess: "", DaysSinceLastSuccess: -1}
	if got := RecommendAction(h); strings.Contains(got, "stale") {
		t.Fatalf("empty LastSuccess should not be library-stale, got %q", got)
	}
}

func TestRecommendAction_OpenTrumpsStale(t *testing.T) {
	h := DriverHealth{
		CircuitState:         "OPEN",
		LastSuccess:          time.Now().UTC().Add(-10 * 24 * time.Hour).Format(time.RFC3339),
		DaysSinceLastSuccess: 10,
		OpenedAt:             time.Now().UTC().Format(time.RFC3339),
	}
	got := RecommendAction(h)
	if strings.Contains(got, "stale") {
		t.Fatalf("OPEN should not report stale, got %q", got)
	}
}

func TestReadDriverHealth_DaysSinceLastSuccess(t *testing.T) {
	dir := t.TempDir()
	ts := time.Now().UTC().Add(-3 * 24 * time.Hour).Format(time.RFC3339)
	if err := WriteHealthFile(dir, "stale-driver", HealthFile{State: "CLOSED", LastSuccess: ts}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadDriverHealth(dir, "stale-driver"); got.DaysSinceLastSuccess != 3 {
		t.Fatalf("want 3 days, got %d", got.DaysSinceLastSuccess)
	}
	if err := WriteHealthFile(dir, "never-ran", HealthFile{State: "CLOSED"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadDriverHealth(dir, "never-ran"); got.DaysSinceLastSuccess != -1 {
		t.Fatalf("want -1, got %d", got.DaysSinceLastSuccess)
	}
}
