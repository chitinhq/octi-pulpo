package pipeline

type Stage string

const (
	StageArchitect Stage = "architect"
	StageImplement Stage = "implement"
	StageQA        Stage = "qa"
	StageReview    Stage = "review"
	StageRelease   Stage = "release"
)

type BackpressureAction struct {
	PauseStage    Stage
	ThrottleStage Stage
	Reason        string
}