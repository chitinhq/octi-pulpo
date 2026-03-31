package pipeline

import "fmt"

const (
	reviewFloodThreshold = 10
	qaFloodThreshold     = 8
	throttledMaxSessions = 3
)

type BackpressureAction struct {
	PauseStage    Stage
	ThrottleStage Stage
	MaxSessions   int
	Reason        string
}

func EvaluateBackpressure(depths map[Stage]int) BackpressureAction {
	if depths[StageReview] > reviewFloodThreshold {
		return BackpressureAction{
			PauseStage: StageArchitect,
			Reason:     fmt.Sprintf("review queue flooding (%d > %d)", depths[StageReview], reviewFloodThreshold),
		}
	}

	if depths[StageQA] > qaFloodThreshold {
		return BackpressureAction{
			ThrottleStage: StageImplement,
			MaxSessions:   throttledMaxSessions,
			Reason:        fmt.Sprintf("qa queue flooding (%d > %d)", depths[StageQA], qaFloodThreshold),
		}
	}

	return BackpressureAction{}
}
