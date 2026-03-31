package pipeline

type Stage string

const (
	StageArchitect Stage = "architect"
	StageImplement Stage = "implement"
	StageQA        Stage = "qa"
	StageReview    Stage = "review"
	StageRelease   Stage = "release"
)

var forwardOrder = []Stage{StageArchitect, StageImplement, StageQA, StageReview, StageRelease}

var validTransitions = map[Stage][]Stage{
	StageArchitect: {StageImplement},
	StageImplement: {StageQA},
	StageQA:        {StageReview, StageImplement},
	StageReview:    {StageRelease, StageImplement, StageArchitect},
	StageRelease:   {},
}

func IsValidTransition(from, to Stage) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	for _, t := range targets {
		if t == to {
			return true
		}
	}
	return false
}

func Label(s Stage) string {
	return "stage:" + string(s)
}

func FromLabel(label string) (Stage, bool) {
	if len(label) < 7 || label[:6] != "stage:" {
		return "", false
	}
	s := Stage(label[6:])
	for _, valid := range forwardOrder {
		if s == valid {
			return s, true
		}
	}
	return "", false
}

func AllLabels() []string {
	labels := make([]string, len(forwardOrder))
	for i, s := range forwardOrder {
		labels[i] = Label(s)
	}
	return labels
}
