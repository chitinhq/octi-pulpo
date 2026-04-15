package routing

type ModelTier string

const (
	TierFrontier ModelTier = "frontier"
	TierMid      ModelTier = "mid"
	TierLight    ModelTier = "light"
	TierNone     ModelTier = "none"
)

// Ladder Forge II (2026-04-14): CLI drivers pruned. TierFree collapsed (was
// goose-only). Remaining tiers map to the surviving substrates:
//   frontier → anthropic (Claude Code Cloud, T3)
//   mid      → gh-actions (T2) + anthropic
//   light    → clawta (T1 local gateway)
var stageTiers = map[string]ModelTier{
	"architect": TierFrontier,
	"implement": TierMid,
	"qa":        TierLight,
	"review":    TierMid,
	"release":   TierNone,
}

const riskEscalationThreshold = 40

var tierDrivers = map[ModelTier][]string{
	TierFrontier: {"anthropic"},
	TierMid:      {"gh-actions", "anthropic"},
	TierLight:    {"clawta"},
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
