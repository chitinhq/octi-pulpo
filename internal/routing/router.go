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
	"openclaw":    TierSubscription,
	"chatgpt":     TierSubscription,
	"notebooklm":  TierSubscription,
	"gemini-app":  TierSubscription,
	// CLI (metered by seat, not per-token)
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI,
	"goose":       TierCLI,
	// API (per-token billing)
	"anthropic":  TierAPI,
	"openai":     TierAPI,
	"gemini-api": TierAPI,
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

// Recommend returns the cheapest capable healthy driver for the given task.
//
// Two constraints are applied:
//   - minTier: capability floor derived from taskType — tiers below this lack
//     the capability to handle the task (e.g. local LLMs for code-review).
//   - maxTier: cost ceiling derived from budget — tiers above this are too
//     expensive for the caller's budget envelope.
//
// Budget values:
//   - "low"    -> cap at local
//   - "medium" -> cap at cli
//   - "high"   -> all tiers (default)
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	minTier := taskTypeMinTier(taskType)
	maxTier := maxTierForBudget(budget)

	// If the capability floor exceeds the budget ceiling the caller has
	// painted themselves into a corner — skip rather than return a driver
	// that can't do the job.
	if tierIndex(minTier) > tierIndex(maxTier) {
		return RouteDecision{
			Skip:   true,
			Reason: fmt.Sprintf("task type %q requires minimum tier %s but budget caps at %s", taskType, minTier, maxTier),
		}
	}

	drivers := DiscoverDrivers(r.healthDir)

	// Build health map from discovered drivers.
	// Only drivers with health files on disk are candidates.
	healthMap := make(map[string]DriverHealth)
	for _, d := range drivers {
		healthMap[d] = ReadDriverHealth(r.healthDir, d)
	}

	var chosen *RouteDecision
	var fallbacks []string

	// Walk tiers in cost order: cheapest capable tier first.
	for _, tier := range tierOrder {
		if tierIndex(tier) < tierIndex(minTier) {
			continue // below capability floor for this task type
		}
		if tierIndex(tier) > tierIndex(maxTier) {
			break // above budget ceiling
		}
		for name, health := range healthMap {
			driverTier := tierFor(name)
			if driverTier != tier {
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
				reason := fmt.Sprintf("cheapest capable driver for %q (tier: %s, state: %s)", taskType, tier, health.CircuitState)
				if taskType == "" {
					reason = fmt.Sprintf("cheapest healthy driver (tier: %s, state: %s)", tier, health.CircuitState)
				}
				chosen = &RouteDecision{
					Driver:     name,
					Tier:       string(tier),
					Confidence: confidence,
					Reason:     reason,
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

// taskTypeMinTier returns the minimum capable tier for a task type.
// Tiers below this floor lack the capability to handle the task reliably.
//
// Mapping rationale (from issue #8 spec):
//   - local:        classification, triage, summarisation, simple tasks
//   - subscription: briefings, research artifacts, document generation
//   - cli:          coding, code-review, PRs, commits, tests
//   - api:          programmatic/burst access requiring raw API semantics
func taskTypeMinTier(taskType string) CostTier {
	lower := strings.ToLower(taskType)
	switch {
	case containsAny(lower, "code", "review", "commit", "test", "refactor", "coding", "debug", "lint", "build") ||
		containsWord(lower, "pr"):
		return TierCLI
	case containsAny(lower, "briefing", "research", "artifact", "document", "report", "notebook"):
		return TierSubscription
	case containsAny(lower, "burst", "programmatic", "batch") || containsWord(lower, "api"):
		return TierAPI
	default:
		// classification, triage, summarisation, simple tasks → local is capable
		return TierLocal
	}
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// containsWord reports whether word appears as a standalone token in s.
// A token is delimited by start/end of string or a non-alphanumeric character.
func containsWord(s, word string) bool {
	if s == word {
		return true
	}
	isDelim := func(r byte) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}
	idx := strings.Index(s, word)
	for idx >= 0 {
		end := idx + len(word)
		beforeOK := idx == 0 || isDelim(s[idx-1])
		afterOK := end == len(s) || isDelim(s[end])
		if beforeOK && afterOK {
			return true
		}
		next := strings.Index(s[idx+1:], word)
		if next < 0 {
			break
		}
		idx = idx + 1 + next
	}
	return false
}
