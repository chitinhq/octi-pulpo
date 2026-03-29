package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestRecommend_AllDriversOpen(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 10})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 8})

	r := NewRouter(dir)
	dec := r.Recommend("anything", "high")

	if !dec.Skip {
		t.Fatalf("expected Skip=true when all drivers OPEN, got driver=%s", dec.Driver)
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

func TestRecommend_NoDriversAvailable(t *testing.T) {
	dir := t.TempDir()
	// Empty directory — no drivers discovered

	r := NewRouter(dir)
	dec := r.Recommend("any-task", "high")

	if !dec.Skip {
		t.Fatalf("expected Skip=true with no drivers, got driver=%s", dec.Driver)
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

// --- Cost-tier cascade tests (issue #8) ---

func TestRecommend_APITierInCatalog(t *testing.T) {
	dir := t.TempDir()
	// Register an API-tier driver; it should be routable.
	writeHealth(t, dir, "claude-api", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("any", "high")

	if dec.Skip {
		t.Fatal("expected recommendation for claude-api, got Skip")
	}
	if dec.Driver != "claude-api" {
		t.Fatalf("expected claude-api, got %s", dec.Driver)
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected tier api, got %s", dec.Tier)
	}
}

func TestRecommend_FullCascade_AllLowerTiersOpen(t *testing.T) {
	dir := t.TempDir()
	// Local and subscription tiers all OPEN; CLI all OPEN; API healthy.
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "openclaw", HealthFile{State: "OPEN", Failures: 3})
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 2})
	writeHealth(t, dir, "claude-api", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("any", "high")

	if dec.Skip {
		t.Fatal("expected API-tier fallback, got Skip")
	}
	if dec.Driver != "claude-api" {
		t.Fatalf("expected claude-api (only healthy driver), got %s", dec.Driver)
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected tier api, got %s", dec.Tier)
	}
	// Cascade reason should mention the skipped tiers.
	if !strings.Contains(dec.Reason, "cascaded past") {
		t.Fatalf("expected cascade reason, got: %s", dec.Reason)
	}
}

func TestRecommend_CascadeReason_MentionsSkippedTiers(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("any", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected CLI tier after local cascade, got %s", dec.Tier)
	}
	if !strings.Contains(dec.Reason, "local") {
		t.Fatalf("cascade reason should mention skipped local tier, got: %s", dec.Reason)
	}
}

func TestRecommend_TaskTypePreference_Coding(t *testing.T) {
	dir := t.TempDir()
	// Both are CLI tier; "coding" preference puts claude-code first.
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("coding", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Driver != "claude-code" {
		t.Fatalf("expected claude-code (preferred for coding), got %s", dec.Driver)
	}
}

func TestRecommend_TaskTypePreference_Classification(t *testing.T) {
	dir := t.TempDir()
	// Both are local tier; "classification" preference puts ollama first.
	writeHealth(t, dir, "nemotron", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("classification", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Driver != "ollama" {
		t.Fatalf("expected ollama (preferred for classification), got %s", dec.Driver)
	}
}

func TestRecommend_TaskTypePreference_SkipsOPENPreferred(t *testing.T) {
	dir := t.TempDir()
	// claude-code is preferred for coding but OPEN; copilot should be chosen.
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 4})
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("coding", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Driver != "copilot" {
		t.Fatalf("expected copilot (preferred but claude-code OPEN), got %s", dec.Driver)
	}
}

func TestRecommend_MediumBudgetIncludesSubscription(t *testing.T) {
	dir := t.TempDir()
	// All local drivers OPEN; openclaw healthy; medium budget should reach subscription tier.
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN", Failures: 3})
	writeHealth(t, dir, "openclaw", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-api", HealthFile{State: "CLOSED", Failures: 0}) // API should be excluded

	r := NewRouter(dir)
	dec := r.Recommend("any", "medium")

	if dec.Skip {
		t.Fatal("expected openclaw recommendation at medium budget, got Skip")
	}
	if dec.Driver != "openclaw" {
		t.Fatalf("expected openclaw, got %s", dec.Driver)
	}
	if dec.Tier != string(TierSubscription) {
		t.Fatalf("expected subscription tier, got %s", dec.Tier)
	}
	// API driver must not appear in fallbacks for medium budget.
	for _, fb := range dec.Fallbacks {
		if fb == "claude-api" {
			t.Fatal("claude-api (API tier) should not appear in fallbacks for medium budget")
		}
	}
}
