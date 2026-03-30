package routing

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CostTier represents the cost category of a driver.
type CostTier string

const (
	TierLocal        CostTier = "local"
	TierSubscription CostTier = "subscription"
	TierCLI          CostTier = "cli"
	TierAPI          CostTier = "api"
)

// tierOrder defines the cost cascade: cheapest first.
var tierOrder = []CostTier{TierLocal, TierSubscription, TierCLI, TierAPI}

// driverTiers maps each known driver to its cost tier.
var driverTiers = map[string]CostTier{
	// Local ($0)
	"ollama":   TierLocal,
	"nemotron": TierLocal,
	// Subscription (browser-based, already paying)
	"openclaw": TierSubscription,
	// CLI (metered subscription)
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI,
	"goose":       TierCLI,
	// API (per-token, burst capacity)
	"claude-api":  TierAPI,
	"openai-api":  TierAPI,
	"gemini-api":  TierAPI,
}

// taskAffinityTiers maps task-type keywords to a minimum cost tier.
// The router will not route below this tier for matching task types, since
// those models may lack the capability required for the task.
var taskAffinityTiers = []struct {
	keywords []string
	minTier  CostTier
}{
	{[]string{"code", "review", "pull-request", "commit", "implement", "debug", "refactor", "test"}, TierCLI},
	{[]string{"browse", "web", "click", "screenshot", "briefing", "artifact", "document"}, TierSubscription},
	{[]string{"burst", "programmatic", "api-call"}, TierAPI},
	// "simple", "classify", "triage" etc. get no override → defaults to TierLocal
}

// DriverHealth represents the runtime health of a single driver.
type DriverHealth struct {
	Name         string `json:"name"`
	CircuitState string `json:"circuit_state"` // CLOSED, OPEN, HALF
	Failures     int    `json:"failures"`
	LastFailure  string `json:"last_failure"`
	LastSuccess  string `json:"last_success"`

	// Enriched fields — populated by ReadDriverHealth from on-disk data.
	OpenedAt       string `json:"opened_at,omitempty"`
	LastSuccessAgo string `json:"last_success_ago,omitempty"`

	// BudgetPct is the estimated remaining budget percentage (0-100).
	// nil means unknown. Populated from Redis by the MCP health_report handler.
	BudgetPct *int `json:"budget_pct,omitempty"`

	// RecommendedAction is a human-readable suggested action for this driver.
	// Populated by RecommendAction(); nil fields in this struct are OK inputs.
	RecommendedAction string `json:"recommended_action,omitempty"`
}

// RouteDecision is the output of the routing engine.
type RouteDecision struct {
	Driver     string   `json:"driver"`
	Tier       string   `json:"tier"`
	Confidence float64  `json:"confidence"`
	Reason     string   `json:"reason"`
	Fallbacks  []string `json:"fallbacks"`
	Skip       bool     `json:"skip"`
}

// Router makes budget-aware driver routing decisions.
type Router struct {
	healthDir string
	tiers     map[string]CostTier // driver → cost tier; defaults to global driverTiers
}

// NewRouter creates a Router that reads driver health from the given directory.
// If healthDir is empty, it defaults to ~/.agentguard/driver-health/.
func NewRouter(healthDir string) *Router {
	if healthDir == "" {
		home, _ := os.UserHomeDir()
		healthDir = filepath.Join(home, ".agentguard", "driver-health")
	}
	return &Router{healthDir: healthDir, tiers: driverTiers}
}

// NewRouterWithTiers creates a Router with an explicit driver→tier map.
// Intended for testing; production code should use NewRouter.
func NewRouterWithTiers(healthDir string, tiers map[string]CostTier) *Router {
	return &Router{healthDir: healthDir, tiers: tiers}
}

// AllHealth returns health status for all discovered drivers in the health directory.
func (r *Router) AllHealth() []DriverHealth {
	return ReadAllHealth(r.healthDir)
}

// Recommend returns the cheapest healthy driver for the given task.
//
// taskType influences the minimum tier considered: coding/review tasks won't
// be routed to local models that lack the required capability. Use an empty
// string for no preference (starts from the cheapest local tier).
//
// budget controls the maximum tier:
//   - "low"    -> local only
//   - "medium" -> local + subscription + cli
//   - "high"   -> all tiers (default)
//   - ""       -> all tiers
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	minTier := taskMinTier(taskType)
	maxTier := maxTierForBudget(budget)

	// If the task requires a tier above what the budget allows, skip immediately
	// rather than routing to an incapable model.
	if tierIndex(minTier) > tierIndex(maxTier) {
		return RouteDecision{
			Skip:   true,
			Reason: fmt.Sprintf("task requires %s tier but budget caps at %s", minTier, maxTier),
		}
	}

	// Collect health for all registered drivers (not just those with health
	// files on disk). A missing health file defaults to CLOSED (healthy).
	healthMap := make(map[string]DriverHealth, len(r.tiers))
	for name := range r.tiers {
		healthMap[name] = ReadDriverHealth(r.healthDir, name)
	}

	var chosen *RouteDecision
	var fallbacks []string

	// Walk tiers in cost order, within [minTier, maxTier].
	for _, tier := range tierOrder {
		idx := tierIndex(tier)
		if idx < tierIndex(minTier) {
			continue // below minimum capability for this task type
		}
		if idx > tierIndex(maxTier) {
			break // above budget cap
		}
		for name, health := range healthMap {
			if r.tierFor(name) != tier {
				continue
			}
			if health.CircuitState == "OPEN" {
				continue // skip exhausted drivers
			}

			confidence := 1.0
			if health.CircuitState == "HALF" {
				confidence = 0.5
			}

			if chosen == nil {
				chosen = &RouteDecision{
					Driver:     name,
					Tier:       string(tier),
					Confidence: confidence,
					Reason:     fmt.Sprintf("cheapest capable driver for %q (tier: %s, state: %s)", taskType, tier, health.CircuitState),
				}
			} else {
				fallbacks = append(fallbacks, name)
			}
		}
	}

	if chosen == nil {
		return RouteDecision{
			Skip:   true,
			Reason: fmt.Sprintf("all drivers exhausted for %q (tiers: %s–%s)", taskType, minTier, maxTier),
		}
	}

	chosen.Fallbacks = fallbacks
	return *chosen
}

// HealthReport returns current health status for all discovered drivers.
func (r *Router) HealthReport() []DriverHealth {
	return ReadAllHealth(r.healthDir)
}

// HealthDir returns the directory where driver health files are stored.
func (r *Router) HealthDir() string {
	return r.healthDir
}

// ForceClose manually resets a driver circuit to CLOSED with zero failures.
// Returns an error if no health file exists for the driver (nothing to reset).
// On success returns the new DriverHealth state.
func (r *Router) ForceClose(driver string) (DriverHealth, error) {
	known := DiscoverDrivers(r.healthDir)
	found := false
	for _, d := range known {
		if d == driver {
			found = true
			break
		}
	}
	if !found {
		return DriverHealth{}, fmt.Errorf("driver %q has no health file in %s", driver, r.healthDir)
	}
	if err := ForceCloseCircuit(r.healthDir, driver); err != nil {
		return DriverHealth{}, err
	}
	return ReadDriverHealth(r.healthDir, driver), nil
}

// DynamicBudget returns a budget level derived from current CLI-tier driver health.
// It is used by the dispatcher to avoid automatic escalation to expensive API-tier
// drivers when CLI-tier capacity is still available.
//
//	"medium" — at least one CLI-tier driver is CLOSED or HALF-OPEN; stay within the
//	           CLI tier and do not escalate to the per-token API tier
//	"low"    — all CLI-tier drivers are OPEN (exhausted); fall back to local/subscription
//
// "high" (which enables API-tier fallback) is never returned automatically.
// Callers that need explicit API-tier burst capacity should pass "high" to Recommend directly.
func (r *Router) DynamicBudget() string {
	healthMap := make(map[string]DriverHealth, len(r.tiers))
	for name := range r.tiers {
		healthMap[name] = ReadDriverHealth(r.healthDir, name)
	}

	var cliHealthy, cliTotal int
	for name, tier := range r.tiers {
		if tier != TierCLI {
			continue
		}
		cliTotal++
		if h := healthMap[name]; h.CircuitState != "OPEN" {
			cliHealthy++
		}
	}

	if cliTotal == 0 || cliHealthy > 0 {
		return "medium" // CLI capacity available; do not escalate to API tier
	}
	return "low" // all CLI-tier drivers exhausted; local/subscription only
}

// taskMinTier returns the minimum cost tier capable of handling the task type.
// Keyword matching is case-insensitive. If no affinity matches, TierLocal is
// returned so the cheapest possible driver is tried first.
func taskMinTier(taskType string) CostTier {
	lower := strings.ToLower(taskType)
	for _, entry := range taskAffinityTiers {
		for _, kw := range entry.keywords {
			if strings.Contains(lower, kw) {
				return entry.minTier
			}
		}
	}
	return TierLocal
}

// maxTierForBudget returns the highest tier to consider for a budget level.
func maxTierForBudget(budget string) CostTier {
	switch strings.ToLower(budget) {
	case "low":
		return TierLocal
	case "medium":
		return TierCLI
	case "high", "":
		return TierAPI
	default:
		return TierAPI
	}
}

// tierFor returns the cost tier for a driver, defaulting to CLI.
func (r *Router) tierFor(driver string) CostTier {
	if t, ok := r.tiers[driver]; ok {
		return t
	}
	return TierCLI // unknown drivers default to CLI tier
}

// tierIndex returns the position in the cost cascade.
func tierIndex(t CostTier) int {
	for i, tier := range tierOrder {
		if tier == t {
			return i
		}
	}
	return len(tierOrder)
}

// RecommendAction returns a human-readable suggested action for a driver based
// on its circuit state, age, and optional budget percentage.
func RecommendAction(h DriverHealth) string {
	budgetLow := h.BudgetPct != nil && *h.BudgetPct < 15

	switch h.CircuitState {
	case "OPEN":
		age := openedAge(h.OpenedAt)
		switch {
		case budgetLow:
			return "driver exhausted — needs budget reset before recovery"
		case age > 60*time.Minute:
			return "circuit open >1h — manual intervention may be needed"
		case age > 30*time.Minute:
			return "circuit open >30min — half-open probe expected soon"
		default:
			return "circuit open — waiting for automatic recovery"
		}
	case "HALF":
		return "half-open — probing, allow one request"
	default: // CLOSED
		if budgetLow {
			return "healthy but budget low — monitor closely"
		}
		return "healthy"
	}
}

// openedAge returns the time elapsed since a driver's circuit was opened.
func openedAge(openedAt string) time.Duration {
	if openedAt == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, openedAt)
	if err != nil {
		return 0
	}
	return time.Since(t)
}
