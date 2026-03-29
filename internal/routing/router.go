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
	// Local ($0) — Ollama / Nemotron via NemoClaw
	"ollama":    TierLocal,
	"nemotron":  TierLocal,
	"nemoclaw":  TierLocal,
	// Subscription (browser-based) — OpenClaw → ChatGPT / NotebookLM / Gemini apps
	"openclaw": TierSubscription,
	// CLI (already paying) — Claude Code / Codex / Copilot / Gemini CLI
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI,
	"goose":       TierCLI,
	// API (per-token) — direct API calls for burst / programmatic access
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
	Driver       string   `json:"driver"`
	Tier         string   `json:"tier"`
	MinTier      string   `json:"min_tier,omitempty"` // task-type minimum tier applied
	Confidence   float64  `json:"confidence"`
	Reason       string   `json:"reason"`
	Fallbacks    []string `json:"fallbacks"`
	CascadeTrace []string `json:"cascade_trace,omitempty"` // why each tier was skipped
	Skip         bool     `json:"skip"`
}

// taskMinTiers maps task-type keywords to the minimum acceptable cost tier.
// Tasks requiring real coding capability skip the local tier; tasks requiring
// per-token burst skip straight to API.
var taskMinTiers = map[string]CostTier{
	// Local-capable: triage, classification, summarisation
	"triage":         TierLocal,
	"classification": TierLocal,
	"summarize":      TierLocal,
	"summarise":      TierLocal,
	"simple":         TierLocal,
	// Subscription-capable: artifact generation, research, briefings
	"research":   TierSubscription,
	"briefing":   TierSubscription,
	"artifact":   TierSubscription,
	"notebooklm": TierSubscription,
	// CLI-capable: coding, PR, commit, review
	"code":          TierCLI,
	"code-review":   TierCLI,
	"commit":        TierCLI,
	"pr":            TierCLI,
	"pull-request":  TierCLI,
	"review":        TierCLI,
	"refactor":      TierCLI,
	"test":          TierCLI,
	// API-only: programmatic burst, batch processing
	"api":         TierAPI,
	"batch":       TierAPI,
	"programmatic": TierAPI,
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
// Budget controls the maximum cost tier:
//   - "low"    -> local only
//   - "medium" -> local + subscription + cli
//   - "high"   -> all tiers (default)
//
// TaskType influences the minimum cost tier — capability-gating:
//   - triage / classification / simple → local is fine
//   - code-review / commit / pr        → skip to CLI (local can't do it)
//   - batch / programmatic             → skip to API
//   - unrecognised                     → local (cheapest)
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	maxTier := maxTierForBudget(budget)
	minTier := minTierForTask(taskType)
	drivers := DiscoverDrivers(r.healthDir)

	// Build health map. Only drivers with health files on disk are candidates.
	healthMap := make(map[string]DriverHealth)
	for _, d := range drivers {
		healthMap[d] = ReadDriverHealth(r.healthDir, d)
	}

	var chosen *RouteDecision
	var fallbacks []string
	var trace []string

	// Walk tiers cheapest-first, cascading upward until we find a healthy driver.
	for _, tier := range tierOrder {
		idx := tierIndex(tier)

		if idx < tierIndex(minTier) {
			trace = append(trace, fmt.Sprintf("skip tier %s: below task minimum (%s)", tier, minTier))
			continue
		}
		if idx > tierIndex(maxTier) {
			trace = append(trace, fmt.Sprintf("stop: tier %s exceeds budget ceiling (%s)", tier, maxTier))
			break
		}

		var openInTier []string
		for name, health := range healthMap {
			if tierFor(name) != tier {
				continue
			}
			if health.CircuitState == "OPEN" {
				openInTier = append(openInTier, name)
				continue
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
					Reason:     fmt.Sprintf("cheapest healthy driver in cascade (tier: %s, state: %s)", tier, health.CircuitState),
				}
			} else {
				fallbacks = append(fallbacks, name)
			}
		}

		if len(openInTier) > 0 && chosen == nil {
			trace = append(trace, fmt.Sprintf("skip tier %s: all drivers OPEN (%s)", tier, strings.Join(openInTier, ", ")))
		}

		// Once we've found a primary pick in this tier, remaining tiers are fallbacks only.
		// Continue walking to collect cross-tier fallbacks.
	}

	if chosen == nil {
		return RouteDecision{
			Skip:         true,
			Reason:       "all drivers exhausted — circuit breakers OPEN or no drivers available",
			CascadeTrace: trace,
		}
	}

	if minTier != TierLocal {
		chosen.MinTier = string(minTier)
	}
	chosen.Fallbacks = fallbacks
	chosen.CascadeTrace = trace
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

// minTierForTask returns the cheapest tier capable of handling the given task type.
// Unrecognised task types default to local (cheapest — no assumption made).
func minTierForTask(taskType string) CostTier {
	if taskType == "" {
		return TierLocal
	}
	// Exact match first.
	if tier, ok := taskMinTiers[strings.ToLower(taskType)]; ok {
		return tier
	}
	// Substring match: "pr-review" contains "review" → CLI, etc.
	lower := strings.ToLower(taskType)
	for keyword, tier := range taskMinTiers {
		if strings.Contains(lower, keyword) {
			return tier
		}
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
