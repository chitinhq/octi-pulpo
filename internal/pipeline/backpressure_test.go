package pipeline

import "testing"

func TestBackpressureDecision(t *testing.T) {
	tests := []struct {
		name         string
		depths       map[Stage]int
		wantPause    Stage
		wantThrottle Stage
	}{
		{
			name:      "review flooding pauses architect",
			depths:    map[Stage]int{StageReview: 12, StageQA: 3, StageImplement: 4},
			wantPause: StageArchitect,
		},
		{
			name:         "qa flooding slows implement",
			depths:       map[Stage]int{StageReview: 2, StageQA: 10, StageImplement: 4},
			wantThrottle: StageImplement,
		},
		{
			name:   "healthy pipeline no action",
			depths: map[Stage]int{StageReview: 2, StageQA: 3, StageImplement: 4},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateBackpressure(tt.depths)
			if got.PauseStage != tt.wantPause {
				t.Errorf("PauseStage = %v, want %v", got.PauseStage, tt.wantPause)
			}
			if got.ThrottleStage != tt.wantThrottle {
				t.Errorf("ThrottleStage = %v, want %v", got.ThrottleStage, tt.wantThrottle)
			}
		})
	}
}
