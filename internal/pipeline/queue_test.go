package pipeline

import "testing"

func TestQueueDepths(t *testing.T) {
	labels := map[string]int{
		"stage:architect":  2,
		"stage:implement":  7,
		"stage:qa":         3,
		"stage:review":     1,
		"stage:release":    0,
	}

	depths := DepthsFromLabelCounts(labels)

	if depths[StageImplement] != 7 {
		t.Errorf("implement depth = %d, want 7", depths[StageImplement])
	}
	if depths[StageArchitect] != 2 {
		t.Errorf("architect depth = %d, want 2", depths[StageArchitect])
	}
}

func TestQueueStarving(t *testing.T) {
	depths := map[Stage]int{
		StageArchitect: 0,
		StageImplement: 1,
		StageQA:        0,
		StageReview:    0,
		StageRelease:   0,
	}

	if !IsStarving(depths) {
		t.Error("expected starving when all downstream queues < 3")
	}

	depths[StageImplement] = 5
	if IsStarving(depths) {
		t.Error("expected not starving when implement queue has 5 items")
	}
}
