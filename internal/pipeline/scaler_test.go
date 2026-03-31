package pipeline

import "testing"

func TestScalerDesiredSessions(t *testing.T) {
	cfg := ScalerConfig{
		MinSessions: map[Stage]int{
			StageArchitect: 0, StageImplement: 1, StageQA: 1, StageReview: 1,
		},
		MaxSessions: map[Stage]int{
			StageArchitect: 3, StageImplement: 8, StageQA: 4, StageReview: 3,
		},
		ScaleUpThreshold: map[Stage]int{
			StageArchitect: 3, StageImplement: 5, StageQA: 3, StageReview: 3,
		},
	}
	scaler := NewScaler(cfg)
	depths := map[Stage]int{StageImplement: 7, StageQA: 2, StageReview: 1}

	desired := scaler.DesiredSessions(depths, BackpressureAction{})
	if desired[StageImplement] < 2 {
		t.Errorf("implement sessions = %d, want >= 2", desired[StageImplement])
	}
	if desired[StageQA] != 1 {
		t.Errorf("qa sessions = %d, want 1", desired[StageQA])
	}
}

func TestScalerRespectsBackpressure(t *testing.T) {
	cfg := ScalerConfig{
		MinSessions:      map[Stage]int{StageArchitect: 0, StageImplement: 1},
		MaxSessions:      map[Stage]int{StageArchitect: 3, StageImplement: 8},
		ScaleUpThreshold: map[Stage]int{StageArchitect: 3, StageImplement: 5},
	}
	scaler := NewScaler(cfg)
	depths := map[Stage]int{StageImplement: 10}

	bp := BackpressureAction{ThrottleStage: StageImplement, MaxSessions: 3}
	desired := scaler.DesiredSessions(depths, bp)

	if desired[StageImplement] > 3 {
		t.Errorf("implement sessions = %d, want <= 3 (throttled)", desired[StageImplement])
	}
}

func TestScalerPausedStage(t *testing.T) {
	cfg := ScalerConfig{
		MinSessions:      map[Stage]int{StageArchitect: 0},
		MaxSessions:      map[Stage]int{StageArchitect: 3},
		ScaleUpThreshold: map[Stage]int{StageArchitect: 3},
	}
	scaler := NewScaler(cfg)
	depths := map[Stage]int{StageArchitect: 5}

	bp := BackpressureAction{PauseStage: StageArchitect}
	desired := scaler.DesiredSessions(depths, bp)

	if desired[StageArchitect] != 0 {
		t.Errorf("architect sessions = %d, want 0 (paused)", desired[StageArchitect])
	}
}
