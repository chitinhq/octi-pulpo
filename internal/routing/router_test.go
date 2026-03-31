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

// cliOnly returns a tier map containing only the given CLI-tier drivers.
// Useful for tests that want a clean, minimal set of candidates.
func cliOnly(drivers ...string) map[string]CostTier {
	m := make(map[string]CostTier, len(drivers))
	for _, d := range drivers {
		m[d] = TierCLI
	}
	return m
}

// tiersFor builds a tier map from name→tier pairs: tiersFor("ollama", TierLocal, "claude-code", TierCLI)
func tiersFor(pairs ...interface{}) map[string]CostTier {
	m := make(map[string]CostTier, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i].(string)] = pairs[i+1].(CostTier)
	}
	return m
}

// ── Existing cascade tests (migrated to NewRouterWithTiers) ─────────────────

func TestRecommend_HealthyDriver(t *testing.T) {
	dir := t.TempDir()
	tiers := cliOnly("claude-code", "copilot")
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED"})
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED"})

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("code-review", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	if dec.Driver == "" {
		t.Fatal("expected a driver name, got empty")
	}
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected tier cli, got %s", dec.Tier)
	}
}

func TestRecommend_SkipsOpenDrivers(t *testing.T) {
	dir := t.TempDir()
	tiers := cliOnly("claude-code", "copilot")
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED"})

	r := NewRouterWithTiers(dir, tiers)
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
	tiers := cliOnly("claude-code", "copilot")
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 10})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 8})

	r := NewRouterWithTiers(dir, tiers)
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
	tiers := tiersFor("ollama", TierLocal, "claude-code", TierCLI)
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED"})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED"})

	r := NewRouterWithTiers(dir, tiers)
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
	if len(dec.Fallbacks) == 0 {
		t.Fatal("expected claude-code as fallback")
	}
}

func TestRecommend_MissingHealthFileDefaultsClosed(t *testing.T) {
	dir := t.TempDir()
	tiers := cliOnly("claude-code", "copilot")
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 3})
	// claude-code has no health file — should default to CLOSED (healthy)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("any-task", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	if dec.Driver != "claude-code" {
		t.Fatalf("expected claude-code (no file = CLOSED), got %s", dec.Driver)
	}
}

func TestRecommend_NoDriversAvailable(t *testing.T) {
	dir := t.TempDir()
	// Empty tier map — no drivers registered at all
	r := NewRouterWithTiers(dir, map[string]CostTier{})
	dec := r.Recommend("any-task", "high")

	if !dec.Skip {
		t.Fatalf("expected Skip=true with no drivers, got driver=%s", dec.Driver)
	}
}

func TestRecommend_LowBudgetOnlyLocal(t *testing.T) {
	dir := t.TempDir()
	tiers := tiersFor("ollama", TierLocal, "claude-code", TierCLI)
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED"})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED"})

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("task", "low")

	if dec.Skip {
		t.Fatal("expected ollama recommendation, got Skip")
	}
	if dec.Driver != "ollama" {
		t.Fatalf("expected ollama (local tier), got %s", dec.Driver)
	}
	for _, fb := range dec.Fallbacks {
		if fb == "claude-code" {
			t.Fatal("claude-code should not be a fallback for low budget")
		}
	}
}

func TestRecommend_LowBudgetAllLocalOpen(t *testing.T) {
	dir := t.TempDir()
	tiers := tiersFor("ollama", TierLocal, "claude-code", TierCLI)
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED"})

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("task", "low")

	if !dec.Skip {
		t.Fatalf("expected Skip (local OPEN, can't use CLI at low budget), got driver=%s", dec.Driver)
	}
}

func TestRecommend_HalfOpenReducedConfidence(t *testing.T) {
	dir := t.TempDir()
	tiers := cliOnly("claude-code")
	writeHealth(t, dir, "claude-code", HealthFile{State: "HALF", Failures: 2})

	r := NewRouterWithTiers(dir, tiers)
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
	tiers := tiersFor("openclaw", TierSubscription, "claude-code", TierCLI)
	writeHealth(t, dir, "openclaw", HealthFile{State: "CLOSED"})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED"})

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("task", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Driver != "openclaw" {
		t.Fatalf("expected openclaw (subscription tier, cheaper), got %s", dec.Driver)
	}
}

// ── API tier tests ────────────────────────────────────────────────────────────

func TestRecommend_APITierDriversRegistered(t *testing.T) {
	// Confirm all expected API tier drivers exist in the global map.
	for _, name := range []string{"claude-api", "openai-api", "gemini-api"} {
		if tier, ok := driverTiers[name]; !ok {
			t.Errorf("driver %q missing from driverTiers", name)
		} else if tier != TierAPI {
			t.Errorf("driver %q: expected TierAPI, got %s", name, tier)
		}
	}
}

func TestRecommend_APITierCascade(t *testing.T) {
	// All cheaper tiers exhausted → should cascade to API.
	dir := t.TempDir()
	tiers := tiersFor(
		"ollama", TierLocal,
		"openclaw", TierSubscription,
		"claude-code", TierCLI,
		"claude-api", TierAPI,
	)
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN"})
	writeHealth(t, dir, "openclaw", HealthFile{State: "OPEN"})
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN"})
	// claude-api has no health file → defaults to CLOSED (healthy)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("any-task", "high")

	if dec.Skip {
		t.Fatalf("expected API tier fallback, got Skip: %s", dec.Reason)
	}
	if dec.Driver != "claude-api" {
		t.Fatalf("expected claude-api (only healthy driver), got %s", dec.Driver)
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected tier api, got %s", dec.Tier)
	}
}

func TestRecommend_APITierNotAllowedAtLowBudget(t *testing.T) {
	dir := t.TempDir()
	tiers := tiersFor("ollama", TierLocal, "claude-api", TierAPI)
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN"})
	// claude-api healthy, but budget is "low"

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("task", "low")

	if !dec.Skip {
		t.Fatalf("expected Skip (budget=low prohibits API tier), got driver=%s", dec.Driver)
	}
}

func TestRecommend_MultipleAPIDrivers_FallbackPopulated(t *testing.T) {
	dir := t.TempDir()
	tiers := tiersFor("claude-api", TierAPI, "openai-api", TierAPI, "gemini-api", TierAPI)
	// All healthy (no health files)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("burst", "high")

	if dec.Skip {
		t.Fatalf("expected recommendation, got Skip")
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected tier api, got %s", dec.Tier)
	}
	if len(dec.Fallbacks) != 2 {
		t.Fatalf("expected 2 fallbacks (other API drivers), got %d: %v", len(dec.Fallbacks), dec.Fallbacks)
	}
}

// ── Task-type affinity tests ──────────────────────────────────────────────────

func TestRecommend_TaskAffinity_CodeReviewPrefersCLI(t *testing.T) {
	dir := t.TempDir()
	tiers := tiersFor("ollama", TierLocal, "claude-code", TierCLI)
	// Both healthy — without affinity, ollama would win; with affinity, claude-code wins.

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("code-review", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected CLI tier for code-review task, got %s (driver=%s)", dec.Tier, dec.Driver)
	}
}

func TestRecommend_TaskAffinity_CommitPrefersCLI(t *testing.T) {
	dir := t.TempDir()
	tiers := tiersFor("ollama", TierLocal, "claude-code", TierCLI)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("commit message generation", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected CLI tier for commit task, got %s", dec.Tier)
	}
}

func TestRecommend_TaskAffinity_BriefingPrefersSubscription(t *testing.T) {
	dir := t.TempDir()
	tiers := tiersFor("ollama", TierLocal, "openclaw", TierSubscription, "claude-code", TierCLI)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("generate briefing document", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Tier != string(TierSubscription) {
		t.Fatalf("expected subscription tier for briefing task, got %s", dec.Tier)
	}
}

func TestRecommend_TaskAffinity_CascadesWhenPreferredTierExhausted(t *testing.T) {
	// Code task prefers CLI, but CLI is OPEN → should cascade to API.
	dir := t.TempDir()
	tiers := tiersFor("claude-code", TierCLI, "claude-api", TierAPI)
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 5})
	// claude-api healthy (no file)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("implement feature", "high")

	if dec.Skip {
		t.Fatalf("expected API tier fallback after CLI exhausted, got Skip: %s", dec.Reason)
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected api tier cascade, got %s (driver=%s)", dec.Tier, dec.Driver)
	}
}

func TestRecommend_TaskAffinity_BudgetCapOverridesAffinityMinTier(t *testing.T) {
	// Code task wants CLI, but budget="low" caps at local → Skip, not silent downgrade.
	dir := t.TempDir()
	tiers := tiersFor("ollama", TierLocal, "claude-code", TierCLI)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("code-review", "low")

	if !dec.Skip {
		t.Fatalf("expected Skip (code-review needs CLI but budget=low), got driver=%s tier=%s", dec.Driver, dec.Tier)
	}
}

func TestRecommend_TaskAffinity_SimpleTaskUsesLocal(t *testing.T) {
	dir := t.TempDir()
	tiers := tiersFor("ollama", TierLocal, "claude-code", TierCLI)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("simple classification", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// "simple" matches no affinity keyword → minTier=local → ollama preferred
	if dec.Tier != string(TierLocal) {
		t.Fatalf("expected local tier for simple task, got %s (driver=%s)", dec.Tier, dec.Driver)
	}
}

func TestTaskMinTier(t *testing.T) {
	cases := []struct {
		taskType string
		want     CostTier
	}{
		{"code-review", TierGHActions},
		{"implement a feature", TierGHActions},
		{"debug the crash", TierGHActions},
		{"run tests", TierGHActions},
		{"commit message", TierGHActions},
		{"open a pull-request", TierGHActions},
		{"generate briefing", TierSubscription},
		{"web screenshot", TierSubscription},
		{"browse the page", TierSubscription},
		// Browser-driver task keywords (issue #5)
		{"generate audio-overview", TierSubscription},
		{"audio overview from documents", TierSubscription},
		{"podcast briefing", TierSubscription},
		{"create slide deck", TierSubscription},
		{"upload document to notebooklm", TierSubscription},
		{"export to drive", TierSubscription},
		{"programmatic api-call", TierAPI},
		{"burst workload", TierAPI},
		{"simple triage", TierLocal},
		{"classify the issue", TierLocal},
		{"", TierLocal},
	}
	for _, tc := range cases {
		got := taskMinTier(tc.taskType)
		if got != tc.want {
			t.Errorf("taskMinTier(%q) = %s, want %s", tc.taskType, got, tc.want)
		}
	}
}

// ── Browser driver routing tests (issue #5) ───────────────────────────────────

func TestBrowserDriversRegistered(t *testing.T) {
	for _, name := range []string{"chatgpt-browser", "notebooklm-browser", "gemini-app-browser"} {
		tier, ok := driverTiers[name]
		if !ok {
			t.Errorf("driver %q missing from driverTiers", name)
			continue
		}
		if tier != TierSubscription {
			t.Errorf("driver %q: expected TierSubscription, got %s", name, tier)
		}
	}
}

func TestRecommend_BrowserDriverPreferredOverCLI(t *testing.T) {
	// Browser tasks should route to subscription-tier browser drivers before CLI.
	dir := t.TempDir()
	tiers := tiersFor(
		"notebooklm-browser", TierSubscription,
		"claude-code", TierCLI,
	)
	// Both healthy (no health files)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("generate audio-overview", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Tier != string(TierSubscription) {
		t.Fatalf("expected subscription tier for audio-overview task, got %s (driver=%s)", dec.Tier, dec.Driver)
	}
	if dec.Driver != "notebooklm-browser" {
		t.Fatalf("expected notebooklm-browser, got %s", dec.Driver)
	}
}

func TestRecommend_BrowserDriverFallsBackToCLI(t *testing.T) {
	// When browser driver circuit is OPEN, task should cascade to CLI.
	dir := t.TempDir()
	tiers := tiersFor(
		"chatgpt-browser", TierSubscription,
		"claude-code", TierCLI,
	)
	writeHealth(t, dir, "chatgpt-browser", HealthFile{State: "OPEN", Failures: 5})
	// claude-code healthy (no health file)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("web screenshot", "high")

	if dec.Skip {
		t.Fatalf("expected CLI fallback, got Skip: %s", dec.Reason)
	}
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected CLI tier fallback when browser OPEN, got %s (driver=%s)", dec.Tier, dec.Driver)
	}
}

func TestRecommend_MultipleBrowserDrivers_FallbackPopulated(t *testing.T) {
	// All three browser drivers healthy → primary + two fallbacks.
	dir := t.TempDir()
	tiers := tiersFor(
		"chatgpt-browser", TierSubscription,
		"notebooklm-browser", TierSubscription,
		"gemini-app-browser", TierSubscription,
	)
	// All healthy (no health files)

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("browse the page", "high")

	if dec.Skip {
		t.Fatalf("expected recommendation, got Skip")
	}
	if dec.Tier != string(TierSubscription) {
		t.Fatalf("expected subscription tier, got %s", dec.Tier)
	}
	if len(dec.Fallbacks) != 2 {
		t.Fatalf("expected 2 fallbacks (other browser drivers), got %d: %v", len(dec.Fallbacks), dec.Fallbacks)
	}
}

func TestRecommend_BrowserDriverNotAllowedAtLowBudget(t *testing.T) {
	// Budget "low" caps at local tier; subscription-tier browser drivers must not be used.
	// (Low budget means local-only, not "use existing subscriptions".)
	dir := t.TempDir()
	tiers := tiersFor(
		"ollama", TierLocal,
		"chatgpt-browser", TierSubscription,
	)
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN"})
	// chatgpt-browser healthy

	r := NewRouterWithTiers(dir, tiers)
	dec := r.Recommend("web screenshot", "low")

	if !dec.Skip {
		t.Fatalf("expected Skip (low budget prohibits subscription tier), got driver=%s tier=%s", dec.Driver, dec.Tier)
	}
}

// ── Health / discovery tests ──────────────────────────────────────────────────

func TestHealthReport(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED"})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 5, LastFailure: "2026-03-29T10:00:00Z"})

	r := NewRouterWithTiers(dir, cliOnly("claude-code", "copilot"))
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

// ── DynamicBudget tests ────────────────────────────────────────────────────────

func TestDynamicBudget_AllCLIHealthy_ReturnsMedium(t *testing.T) {
	dir := t.TempDir()
	tiers := cliOnly("claude-code", "copilot", "codex")
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED"})
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED"})
	writeHealth(t, dir, "codex", HealthFile{State: "CLOSED"})

	r := NewRouterWithTiers(dir, tiers)
	got := r.DynamicBudget()
	if got != "medium" {
		t.Fatalf("expected medium (CLI healthy), got %s", got)
	}
}

func TestDynamicBudget_SomeCLIOpen_ReturnsMedium(t *testing.T) {
	// Even with one healthy CLI driver, budget stays at medium (don't escalate to API).
	dir := t.TempDir()
	tiers := cliOnly("claude-code", "copilot", "codex")
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED"})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN"})
	writeHealth(t, dir, "codex", HealthFile{State: "OPEN"})

	r := NewRouterWithTiers(dir, tiers)
	got := r.DynamicBudget()
	if got != "medium" {
		t.Fatalf("expected medium (claude-code still healthy), got %s", got)
	}
}

func TestDynamicBudget_AllCLIOpen_ReturnsLow(t *testing.T) {
	dir := t.TempDir()
	tiers := cliOnly("claude-code", "copilot", "codex")
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN"})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN"})
	writeHealth(t, dir, "codex", HealthFile{State: "OPEN"})

	r := NewRouterWithTiers(dir, tiers)
	got := r.DynamicBudget()
	if got != "low" {
		t.Fatalf("expected low (all CLI exhausted), got %s", got)
	}
}

func TestDynamicBudget_HalfOpenCLI_ReturnsMedium(t *testing.T) {
	// HALF-OPEN is not OPEN, so the driver is still considered available.
	dir := t.TempDir()
	tiers := cliOnly("claude-code")
	writeHealth(t, dir, "claude-code", HealthFile{State: "HALF"})

	r := NewRouterWithTiers(dir, tiers)
	got := r.DynamicBudget()
	if got != "medium" {
		t.Fatalf("expected medium (HALF is not exhausted), got %s", got)
	}
}

func TestDynamicBudget_NoCLITiers_ReturnsMedium(t *testing.T) {
	// If no CLI drivers are registered (e.g. local-only test env),
	// fall back to "medium" as safe default.
	dir := t.TempDir()
	tiers := map[string]CostTier{"ollama": TierLocal}

	r := NewRouterWithTiers(dir, tiers)
	got := r.DynamicBudget()
	if got != "medium" {
		t.Fatalf("expected medium (no CLI drivers registered), got %s", got)
	}
}

func TestDynamicBudget_MissingHealthFile_DefaultsClosed_Medium(t *testing.T) {
	// A CLI driver with no health file defaults to CLOSED (healthy).
	dir := t.TempDir()
	tiers := cliOnly("claude-code") // no health file written

	r := NewRouterWithTiers(dir, tiers)
	got := r.DynamicBudget()
	if got != "medium" {
		t.Fatalf("expected medium (missing health file = CLOSED), got %s", got)
	}
}
