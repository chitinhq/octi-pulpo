package routing

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	// Local ($0) — Ollama, NemoClaw-hosted Nemotron
	"ollama":   TierLocal,
	"nemotron": TierLocal,
	// Subscription (browser-based, already paying) — OpenClaw browser runtime
	"openclaw": TierSubscription,
	// CLI (already paying) — local tooling, no per-token cost
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI,
	"goose":       TierCLI,
	// API (per-token) — burst capacity, programmatic access
	"claude-api": TierAPI,
	"openai-api": TierAPI,
	"gemini-api": TierAPI,
}

// taskPreferences maps task types to drivers in preferred order within a tier.
// Drivers earlier in the slice are selected first when multiple healthy options exist.
var taskPreferences = map[string][]string{
	"coding":         {"claude-code", "codex", "copilot", "gemini"},
	"code-review":    {"claude-code", "copilot", "codex"},
	"classification": {"ollama", "nemotron"},
	"brief":          {"openclaw"},
	"summary":        {"openclaw", "ollama"},
	"agentic":        {"goose", "claude-code"},
}

// candidate is a driver name paired with its current health state.
type candidate struct {
	name   string
	health DriverHealth
}

// tieredCandidates returns all drivers for a cost tier sorted by task preference then name.
// Preferred drivers (matching taskType prefs) sort before unpreferred ones.
func tieredCandidates(healthMap map[string]DriverHealth, tier CostTier, prefs []string) []candidate {
	prefRank := make(map[string]int, len(prefs))
	for i, p := range prefs {
		prefRank[p] = i + 1 // 1-based; 0 means "not preferred"
	}

	var result []candidate
	for name, health := range healthMap {
		if tierFor(name) == tier {
			result = append(result, candidate{name, health})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		ri, rj := prefRank[result[i].name], prefRank[result[j].name]
		switch {
		case ri != 0 && rj != 0:
			return ri < rj // both preferred: lower rank wins
		case ri != 0:
			return true // only i preferred
		case rj != 0:
			return false // only j preferred
		default:
			return result[i].name < result[j].name // stable tie-break
		}
	})
	return result
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
// The budget parameter controls which cost tiers are considered:
//   - "low"    -> local only
//   - "medium" -> local + subscription + cli
//   - "high"   -> all tiers
//   - ""       -> all tiers (default)
//
// Within a tier, drivers are ordered by taskType preference (see taskPreferences).
// When all drivers in a tier have OPEN circuit breakers the cascade continues to the
// next more-expensive tier and that skip is recorded in the returned Reason.
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	maxTier := maxTierForBudget(budget)

	// Build health map from discovered drivers only.
	// A health file on disk is the registration signal for a driver.
	drivers := DiscoverDrivers(r.healthDir)
	healthMap := make(map[string]DriverHealth, len(drivers))
	for _, d := range drivers {
		healthMap[d] = ReadDriverHealth(r.healthDir, d)
	}

	prefs := taskPreferences[taskType]

	var chosen *RouteDecision
	var fallbacks []string
	var skippedTiers []string

	// Walk tiers cheapest-first; collect fallbacks across all healthy drivers.
	for _, tier := range tierOrder {
		if tierIndex(tier) > tierIndex(maxTier) {
			break
		}
		candidates := tieredCandidates(healthMap, tier, prefs)
		if len(candidates) == 0 {
			continue // no registered drivers for this tier
		}

		tierSkipped := true
		for _, c := range candidates {
			if c.health.CircuitState == "OPEN" {
				continue
			}
			tierSkipped = false

			confidence := 1.0
			if c.health.CircuitState == "HALF" {
				confidence = 0.5
			}

			if chosen == nil {
				reason := fmt.Sprintf("cheapest healthy driver (tier: %s, circuit: %s)", tier, c.health.CircuitState)
				if len(skippedTiers) > 0 {
					reason += fmt.Sprintf("; cascaded past: %s", strings.Join(skippedTiers, ", "))
				}
				chosen = &RouteDecision{
					Driver:     c.name,
					Tier:       string(tier),
					Confidence: confidence,
					Reason:     reason,
				}
			} else {
				fallbacks = append(fallbacks, c.name)
			}
		}

		// Record tiers where every registered driver was OPEN (so the caller can
		// understand why a more-expensive tier was chosen).
		if tierSkipped && chosen == nil {
			skippedTiers = append(skippedTiers, string(tier))
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
