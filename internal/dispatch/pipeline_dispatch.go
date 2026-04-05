package dispatch

import (
	"github.com/chitinhq/octi-pulpo/internal/routing"
)

// PipelineRouter selects the best driver for a pipeline stage based on
// the stage's model-tier requirement and current driver health.
type PipelineRouter struct{}

// RouteForStage returns a routing decision for the given pipeline stage.
// It resolves the stage to a model tier (factoring in risk score), then
// picks the cheapest healthy driver from that tier's candidate list.
func (pr *PipelineRouter) RouteForStage(stage string, riskScore int, health []routing.DriverHealth) routing.RouteDecision {
	tier := routing.TierForStageWithRisk(stage, riskScore)

	if tier == routing.TierNone {
		return routing.RouteDecision{
			Tier:   string(tier),
			Reason: "stage is automated, no driver needed",
			Skip:   true,
		}
	}

	candidates := routing.DriversForTier(tier)

	healthMap := make(map[string]bool)
	for _, h := range health {
		if h.CircuitState == "CLOSED" || h.CircuitState == "HALF" {
			healthMap[h.Name] = true
		}
	}

	var fallbacks []string
	for _, driver := range candidates {
		if healthMap[driver] {
			return routing.RouteDecision{
				Driver:     driver,
				Tier:       string(tier),
				Confidence: 0.9,
				Reason:     "cheapest healthy driver for " + string(tier) + " tier",
				Fallbacks:  fallbacks,
			}
		}
		fallbacks = append(fallbacks, driver)
	}

	return routing.RouteDecision{
		Skip:   true,
		Tier:   string(tier),
		Reason: "no healthy driver for " + string(tier) + " tier",
	}
}
