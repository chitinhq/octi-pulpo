package pipeline

const starvingThreshold = 3

func DepthsFromLabelCounts(labelCounts map[string]int) map[Stage]int {
	depths := make(map[Stage]int)
	for label, count := range labelCounts {
		stage, ok := FromLabel(label)
		if ok {
			depths[stage] = count
		}
	}
	return depths
}

func IsStarving(depths map[Stage]int) bool {
	return depths[StageImplement] < starvingThreshold &&
		depths[StageQA] < starvingThreshold &&
		depths[StageReview] < starvingThreshold
}

func TotalPending(depths map[Stage]int) int {
	total := 0
	for _, d := range depths {
		total += d
	}
	return total
}
