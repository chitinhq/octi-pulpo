package cogrouter

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testConfigPath locates config/router.yaml relative to the repo root by
// walking up from this test file's directory.
func testConfigPath(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// file = .../internal/cogrouter/router_test.go → repo root is two dirs up.
	return filepath.Join(filepath.Dir(file), "..", "..", "config", "router.yaml")
}

func loadTestRouter(t *testing.T) *Router {
	t.Helper()
	cfg, err := LoadRules(testConfigPath(t))
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestLoadRulesShipped(t *testing.T) {
	cfg, err := LoadRules(testConfigPath(t))
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if cfg.Default.Soul == "" {
		t.Fatal("default soul empty")
	}
	if len(cfg.Rules) == 0 {
		t.Fatal("no rules loaded")
	}
}

func TestRouteDebugging(t *testing.T) {
	r := loadTestRouter(t)
	d, err := r.Route(TaskContext{ID: "T1", Type: "debugging"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Soul != "feynman" {
		t.Errorf("want soul=feynman, got %q", d.Soul)
	}
	if d.ChitinProfile != "diagnostic_strict" {
		t.Errorf("want chitin=diagnostic_strict, got %q", d.ChitinProfile)
	}
	if d.Mode != "investigate" {
		t.Errorf("want mode=investigate, got %q", d.Mode)
	}
	if d.RuleID != "debugging" {
		t.Errorf("want rule_id=debugging, got %q", d.RuleID)
	}
}

func TestRouteProtectedWorkflowsWinsOverType(t *testing.T) {
	r := loadTestRouter(t)
	// Even with type=algorithmic, the workflow-path rule appears first and wins.
	d, err := r.Route(TaskContext{
		ID:           "T2",
		Type:         "algorithmic",
		TouchedPaths: []string{".github/workflows/ci.yml"},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Soul != "turing" {
		t.Errorf("want soul=turing, got %q", d.Soul)
	}
	if d.ChitinProfile != "protected_repo" {
		t.Errorf("want chitin=protected_repo, got %q", d.ChitinProfile)
	}
	if !d.RequireReview {
		t.Error("want require_review=true")
	}
}

func TestRouteCriticalRisk(t *testing.T) {
	r := loadTestRouter(t)
	d, err := r.Route(TaskContext{ID: "T3", Type: "refactor", Risk: "critical"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	// Risk=critical rule precedes the refactor rule in config order.
	if d.Soul != "dijkstra" {
		t.Errorf("want soul=dijkstra, got %q", d.Soul)
	}
	if d.ChitinProfile != "infra_critical" {
		t.Errorf("want chitin=infra_critical, got %q", d.ChitinProfile)
	}
}

func TestRouteDefault(t *testing.T) {
	r := loadTestRouter(t)
	d, err := r.Route(TaskContext{ID: "T4"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.RuleID != "default" {
		t.Errorf("want rule_id=default, got %q", d.RuleID)
	}
	if d.Soul != "jared_pleva" {
		t.Errorf("want default soul=jared_pleva, got %q", d.Soul)
	}
	if d.Confidence != 1.0 {
		t.Errorf("want confidence=1.0, got %v", d.Confidence)
	}
	if len(d.Rationale) == 0 {
		t.Error("want non-empty rationale")
	}
}

func TestFirstMatchWins(t *testing.T) {
	cfg := &Config{
		Default: Assign{Soul: "x", BodyProfile: "b", ChitinProfile: "c", Mode: "m"},
		Rules: []Rule{
			{ID: "a", When: Match{Type: "t"}, Assign: Assign{Soul: "A"}},
			{ID: "b", When: Match{Type: "t"}, Assign: Assign{Soul: "B"}},
		},
	}
	r, _ := New(cfg)
	d, _ := r.Route(TaskContext{Type: "t"})
	if d.RuleID != "a" || d.Soul != "A" {
		t.Errorf("want first match (a/A), got %s/%s", d.RuleID, d.Soul)
	}
}

func TestParseRulesErrors(t *testing.T) {
	_, err := ParseRules([]byte("rules: []\n"))
	if err == nil || !strings.Contains(err.Error(), "default.soul") {
		t.Errorf("want missing-default error, got %v", err)
	}
}
