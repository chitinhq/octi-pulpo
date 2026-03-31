package pipeline

type ScalerConfig struct {
	MinSessions      map[Stage]int
	MaxSessions      map[Stage]int
	ScaleUpThreshold map[Stage]int
}

type Scaler struct {
	cfg ScalerConfig
}

func NewScaler(cfg ScalerConfig) *Scaler {
	return &Scaler{cfg: cfg}
}

func (s *Scaler) DesiredSessions(depths map[Stage]int, bp BackpressureAction) map[Stage]int {
	desired := make(map[Stage]int)

	for _, stage := range forwardOrder {
		if stage == StageRelease {
			continue
		}

		min := s.cfg.MinSessions[stage]
		max := s.cfg.MaxSessions[stage]
		threshold := s.cfg.ScaleUpThreshold[stage]
		depth := depths[stage]

		if bp.PauseStage == stage {
			desired[stage] = 0
			continue
		}

		effectiveMax := max
		if bp.ThrottleStage == stage && bp.MaxSessions < max {
			effectiveMax = bp.MaxSessions
		}

		sessions := min
		if depth > 0 && threshold > 0 {
			sessions = (depth + threshold - 1) / threshold
		}

		if sessions < min {
			sessions = min
		}
		if sessions > effectiveMax {
			sessions = effectiveMax
		}

		desired[stage] = sessions
	}

	return desired
}
