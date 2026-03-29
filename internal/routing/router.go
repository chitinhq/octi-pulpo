package routing

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// CLI (metered, already paying)
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI,
	"goose":       TierCLI,
	// API (per-token, burst capacity)
	"anthropic-api": TierAPI,
	"openai-api":    TierAPI,
	"gemini-api":    TierAPI,
}

// taskMinTier maps well-known task types to the cheapest tier that can handle them.
// Tasks requiring richer models skip tiers below their minimum.
// Unknown task types default to TierLocal (try cheapest first).
var taskMinTier = map[string]CostTier{
	// Simple tasks — local models are sufficient
	"classification": TierLocal,
	"triage":         TierLocal,
	"routing":        TierLocal,
	// Artifact / briefing tasks — need richer models than local
	"briefing":  TierSubscription,
	"artifact":  TierSubscription,
	"summarize": TierSubscription,
	// Coding tasks — require CLI-grade models
	"code-review": TierCLI,
	"pr":          TierCLI,
	"commit":      TierCLI,
	"coding":      TierCLI,
	"refactor":    TierCLI,
	// Burst / programmatic — skip straight to API tier
	"burst": TierAPI,
}

// DriverHealth represents the runtime health of a single driver.
type DriverHealth struct {
	Name         string `json:"name"`
	CircuitState string `json:"circuit_state"` // CLOSED, OPEN, HALF
	Failures     int    `json:"failures"`
	LastFailure  string `json:"last_failure"`
	LastSuccess  string `json:"last_success"`
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
}

// NewRouter creates a Router that reads driver health from the given directory.
// If healthDir is empty, it defaults to ~/.agentguard/driver-health/.
func NewRouter(healthDir string) *Router {
	if healthDir == "" {
		home, _ := os.UserHomeDir()
		healthDir = filepath.Join(home, ".agentguard", "driver-health")
	}
	return &Router{healthDir: healthDir}
}

// Recommend returns the cheapest healthy driver for the given task.
//
// Budget controls which cost tiers are considered (upper bound):
//   - "low"    -> local only
//   - "medium" -> local + subscription + cli
//   - "high"   -> all tiers (default when empty)
//
// TaskType controls which tiers are skipped (lower bound): certain task types
// require richer models and skip cheaper tiers that can't handle them.
// For example, "code-review" skips local and starts from CLI tier.
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	maxTier := maxTierForBudget(budget)
	minTier := minTierForTask(taskType)
	drivers := DiscoverDrivers(r.healthDir)

	// Build health map from discovered drivers.
	// Only drivers with health files on disk are candidates.
	healthMap := make(map[string]DriverHealth)
	for _, d := range drivers {
		healthMap[d] = ReadDriverHealth(r.healthDir, d)
	}

	var chosen *RouteDecision
	var fallbacks []string

	// Walk tiers in cost order: cheapest first, bounded by task minimum and budget maximum.
	for _, tier := range tierOrder {
		if tierIndex(tier) < tierIndex(minTier) {
			continue // skip tiers too cheap for this task type
		}
		if tierIndex(tier) > tierIndex(maxTier) {
			break // stop at budget ceiling
		}
		for name, health := range healthMap {
			if tierFor(name) != tier {
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
					Reason:     fmt.Sprintf("cheapest eligible driver (tier: %s, state: %s)", tier, health.CircuitState),
				}
			} else {
				fallbacks = append(fallbacks, name)
			}
		}
	}

	if chosen == nil {
		return RouteDecision{
			Skip:   true,
			Reason: "all drivers exhausted — circuit breakers OPEN",
		}
	}

	chosen.Fallbacks = fallbacks
	return *chosen
}

// HealthReport returns current health status for all discovered drivers.
func (r *Router) HealthReport() []DriverHealth {
	return ReadAllHealth(r.healthDir)
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

// minTierForTask returns the cheapest tier that can handle the given task type.
// Unknown task types default to TierLocal (try cheapest first).
func minTierForTask(taskType string) CostTier {
	if t, ok := taskMinTier[strings.ToLower(taskType)]; ok {
		return t
	}
	return TierLocal
}

// tierFor returns the cost tier for a driver, defaulting to CLI.
func tierFor(driver string) CostTier {
	if t, ok := driverTiers[driver]; ok {
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
