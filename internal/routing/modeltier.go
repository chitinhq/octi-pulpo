package routing

type ModelTier string

const (
	TierFrontier ModelTier = "frontier"
	TierMid      ModelTier = "mid"
	TierLight    ModelTier = "light"
	TierFree     ModelTier = "free"
	TierNone     ModelTier = "none"
)

var stageTiers = map[string]ModelTier{
	"architect": TierFrontier,
	"implement": TierMid,
	"qa":        TierLight,
	"review":    TierMid,
	"release":   TierNone,
}

const riskEscalationThreshold = 40

var tierDrivers = map[ModelTier][]string{
	TierFrontier: {"claude-code", "copilot"},
	TierMid:      {"copilot", "codex", "gemini", "claude-code"},
	TierLight:    {"claude-code", "codex", "gemini"},
	TierFree:     {"goose"},
	TierNone:     {},
}

func TierForStage(stage string) ModelTier {
	if t, ok := stageTiers[stage]; ok {
		return t
	}
	return TierMid
}

func TierForStageWithRisk(stage string, riskScore int) ModelTier {
	base := TierForStage(stage)
	if stage == "review" && riskScore > riskEscalationThreshold {
		return TierFrontier
	}
	return base
}

func DriversForTier(tier ModelTier) []string {
	if drivers, ok := tierDrivers[tier]; ok {
		return drivers
	}
	return nil
}
