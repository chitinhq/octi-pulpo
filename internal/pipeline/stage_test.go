package pipeline

import "testing"

func TestValidTransitions(t *testing.T) {
	tests := []struct {
		from, to Stage
		valid    bool
	}{
		{StageArchitect, StageImplement, true},
		{StageImplement, StageQA, true},
		{StageQA, StageReview, true},
		{StageReview, StageRelease, true},
		{StageQA, StageImplement, true},
		{StageReview, StageImplement, true},
		{StageReview, StageArchitect, true},
		{StageArchitect, StageReview, false},
		{StageQA, StageRelease, false},
		{StageRelease, StageImplement, false},
	}

	for _, tt := range tests {
		result := IsValidTransition(tt.from, tt.to)
		if result != tt.valid {
			t.Errorf("transition %s→%s: got %v, want %v", tt.from, tt.to, result, tt.valid)
		}
	}
}

func TestStageLabel(t *testing.T) {
	if Label(StageImplement) != "stage:implement" {
		t.Errorf("got %s, want stage:implement", Label(StageImplement))
	}
}

func TestStageFromLabel(t *testing.T) {
	s, ok := FromLabel("stage:qa")
	if !ok || s != StageQA {
		t.Errorf("got %v/%v, want StageQA/true", s, ok)
	}
	_, ok = FromLabel("bug")
	if ok {
		t.Error("expected false for non-stage label")
	}
}
