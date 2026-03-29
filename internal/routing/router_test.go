package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeHealth writes a driver health JSON file into the temp directory.
func writeHealth(t *testing.T, dir, driver string, hf HealthFile) {
	t.Helper()
	data, err := json.Marshal(hf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, driver+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRecommend_HealthyDriver(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("code-review", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	if dec.Driver == "" {
		t.Fatal("expected a driver name, got empty")
	}
	// Both are CLI tier — either is valid
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected tier cli, got %s", dec.Tier)
	}
}

func TestRecommend_SkipsOpenDrivers(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("code-review", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	if dec.Driver != "copilot" {
		t.Fatalf("expected copilot (healthy), got %s", dec.Driver)
	}
}

func TestRecommend_CLIDriversOpen_FallsToAPI(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 10})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 8})

	r := NewRouter(dir)
	dec := r.Recommend("anything", "high")

	// CLI drivers OPEN — cascade must fall through to API tier.
	if dec.Skip {
		t.Fatal("expected API tier fallback when CLI drivers OPEN, got Skip")
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected API tier fallback, got tier=%s driver=%s", dec.Tier, dec.Driver)
	}
}

func TestRecommend_AllTiersExhausted_Skip(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 10})
	writeHealth(t, dir, "anthropic-api", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "openai-api", HealthFile{State: "OPEN", Failures: 3})

	r := NewRouter(dir)
	dec := r.Recommend("anything", "high")

	if !dec.Skip {
		t.Fatalf("expected Skip when all tiers exhausted, got driver=%s tier=%s", dec.Driver, dec.Tier)
	}
	if dec.Reason == "" {
		t.Fatal("expected a reason when skipping")
	}
}

func TestRecommend_CostTierOrdering(t *testing.T) {
	dir := t.TempDir()
	// Local driver should be chosen over CLI when both healthy
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("simple-task", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	if dec.Driver != "ollama" {
		t.Fatalf("expected ollama (cheapest tier), got %s", dec.Driver)
	}
	if dec.Tier != string(TierLocal) {
		t.Fatalf("expected tier local, got %s", dec.Tier)
	}
	// claude-code should be a fallback
	if len(dec.Fallbacks) == 0 {
		t.Fatal("expected claude-code as fallback")
	}
}

func TestRecommend_MissingHealthFileDefaultsClosed(t *testing.T) {
	dir := t.TempDir()
	// Write a valid file for copilot (OPEN), but claude-code has no file
	// Manually create a file with missing/empty state
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 3})
	writeHealth(t, dir, "claude-code", HealthFile{}) // empty state defaults to CLOSED in ReadDriverHealth

	r := NewRouter(dir)
	dec := r.Recommend("any-task", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	// claude-code should be chosen since copilot is OPEN
	// and empty state in ReadDriverHealth becomes whatever the file says (empty string)
	// We need to check: empty state should be treated as healthy
	if dec.Driver != "claude-code" {
		t.Fatalf("expected claude-code (copilot is OPEN), got %s", dec.Driver)
	}
}

func TestRecommend_NoLocalCLIDrivers_FallsToAPI(t *testing.T) {
	dir := t.TempDir()
	// No health files for local/subscription/CLI drivers —
	// API tier is always seeded and should be the last-resort fallback.

	r := NewRouter(dir)
	dec := r.Recommend("any-task", "high")

	if dec.Skip {
		t.Fatal("expected API tier fallback when no other drivers registered, got Skip")
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected API tier fallback, got tier=%s driver=%s", dec.Tier, dec.Driver)
	}
}

func TestRecommend_LowBudgetOnlyLocal(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("task", "low")

	// Low budget only allows local tier — should pick ollama, skip claude-code
	if dec.Skip {
		t.Fatal("expected ollama recommendation, got Skip")
	}
	if dec.Driver != "ollama" {
		t.Fatalf("expected ollama (local tier), got %s", dec.Driver)
	}
	// claude-code should NOT be in fallbacks (it's CLI tier, above budget)
	for _, fb := range dec.Fallbacks {
		if fb == "claude-code" {
			t.Fatal("claude-code should not be a fallback for low budget")
		}
	}
}

func TestRecommend_LowBudgetAllLocalOpen(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("task", "low")

	// ollama OPEN, and low budget prevents using CLI tier claude-code
	if !dec.Skip {
		t.Fatalf("expected Skip (local OPEN, can't use CLI at low budget), got driver=%s", dec.Driver)
	}
}

func TestRecommend_HalfOpenReducedConfidence(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "HALF", Failures: 2})

	r := NewRouter(dir)
	dec := r.Recommend("task", "high")

	if dec.Skip {
		t.Fatal("expected a recommendation for HALF-open driver")
	}
	if dec.Driver != "claude-code" {
		t.Fatalf("expected claude-code, got %s", dec.Driver)
	}
	if dec.Confidence != 0.5 {
		t.Fatalf("expected confidence 0.5 for HALF driver, got %f", dec.Confidence)
	}
}

func TestRecommend_SubscriptionTier(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "openclaw", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("task", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// openclaw (subscription) is cheaper than claude-code (cli)
	if dec.Driver != "openclaw" {
		t.Fatalf("expected openclaw (subscription tier, cheaper), got %s", dec.Driver)
	}
}

func TestHealthReport(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 5, LastFailure: "2026-03-29T10:00:00Z"})

	r := NewRouter(dir)
	report := r.HealthReport()

	if len(report) != 2 {
		t.Fatalf("expected 2 drivers in report, got %d", len(report))
	}

	found := make(map[string]DriverHealth)
	for _, dh := range report {
		found[dh.Name] = dh
	}

	if cc, ok := found["claude-code"]; !ok {
		t.Fatal("missing claude-code in report")
	} else if cc.CircuitState != "CLOSED" {
		t.Fatalf("expected CLOSED for claude-code, got %s", cc.CircuitState)
	}

	if cp, ok := found["copilot"]; !ok {
		t.Fatal("missing copilot in report")
	} else if cp.CircuitState != "OPEN" {
		t.Fatalf("expected OPEN for copilot, got %s", cp.CircuitState)
	} else if cp.Failures != 5 {
		t.Fatalf("expected 5 failures for copilot, got %d", cp.Failures)
	}
}

func TestDiscoverDrivers_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	drivers := DiscoverDrivers(dir)
	if len(drivers) != 0 {
		t.Fatalf("expected 0 drivers in empty dir, got %d", len(drivers))
	}
}

func TestDiscoverDrivers_NonexistentDir(t *testing.T) {
	drivers := DiscoverDrivers("/nonexistent/path/that/does/not/exist")
	if drivers != nil {
		t.Fatalf("expected nil for nonexistent dir, got %v", drivers)
	}
}

// TestRecommend_APITierAlwaysAvailable verifies that API drivers are candidates
// even when they have no health file on disk, enabling the cascade to reach API
// tier without requiring an explicit registration step.
func TestRecommend_APITierAlwaysAvailable(t *testing.T) {
	dir := t.TempDir()
	// Only a CLI driver health file — no API health files written.
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 3})

	r := NewRouter(dir)
	dec := r.Recommend("task", "high")

	if dec.Skip {
		t.Fatal("expected API tier fallback even without API health files, got Skip")
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected API tier, got tier=%s driver=%s", dec.Tier, dec.Driver)
	}
}

// TestRecommend_DeterministicSelection verifies that repeated calls with identical
// state always return the same driver (no map-iteration randomness).
func TestRecommend_DeterministicSelection(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED"})
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED"})
	// Suppress cheaper tiers so selection stays in CLI.
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN"})
	writeHealth(t, dir, "nemotron", HealthFile{State: "OPEN"})
	writeHealth(t, dir, "openclaw", HealthFile{State: "OPEN"})

	r := NewRouter(dir)
	first := r.Recommend("task", "medium") // medium budget: local+subscription+cli, no API
	second := r.Recommend("task", "medium")

	if first.Driver != second.Driver {
		t.Fatalf("non-deterministic: got %s then %s", first.Driver, second.Driver)
	}
	// Alphabetically first healthy CLI driver is claude-code.
	if first.Driver != "claude-code" {
		t.Fatalf("expected claude-code (alphabetically first in CLI tier), got %s", first.Driver)
	}
}

// TestRecommend_APIDriverMarkedOpen verifies that an API driver with an OPEN
// health file is correctly skipped during the cascade.
func TestRecommend_APIDriverMarkedOpen(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN"})
	writeHealth(t, dir, "anthropic-api", HealthFile{State: "OPEN"})
	// openai-api has no health file — defaults to CLOSED.

	r := NewRouter(dir)
	dec := r.Recommend("task", "high")

	if dec.Skip {
		t.Fatal("expected openai-api as fallback, got Skip")
	}
	if dec.Driver != "openai-api" {
		t.Fatalf("expected openai-api (only healthy API driver), got %s", dec.Driver)
	}
}
