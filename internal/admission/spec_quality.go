package admission

import "strings"

// ArchitectSpec is the output produced by the architect stage that must be
// validated before a task advances to stage:implement.
type ArchitectSpec struct {
	// Title is the original issue/task title.
	Title string `json:"title"`
	// AcceptanceCriteria lists the conditions that must be true for the task
	// to be considered complete. Required — must have at least one entry.
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	// FilesTouched lists the files the implementor is expected to modify.
	// Required — prevents blind blast-radius surprises.
	FilesTouched []string `json:"files_touched"`
	// BlastRadiusEstimate is the architect's estimate of files changed.
	// Required — used by the admission gate to set concurrency limits.
	BlastRadiusEstimate int `json:"blast_radius_estimate"`
	// Approach is a prose description of the implementation strategy.
	// Required — must be non-empty and specific (no generic placeholder text).
	Approach string `json:"approach"`
}

// SpecQualityResult is the output of ScoreSpec.
type SpecQualityResult struct {
	// Ready is true when the spec passes all quality checks.
	Ready bool `json:"ready"`
	// Score is a 0.0–1.0 composite where 1.0 means all fields are present
	// and complete. Score < 0.7 routes back to stage:architect with feedback.
	Score float64 `json:"score"`
	// Missing lists the field names that are absent or incomplete.
	Missing []string `json:"missing,omitempty"`
	// Feedback is a human-readable message to send back to the architect agent.
	Feedback string `json:"feedback"`
}

// ambiguousPlaceholders are strings that indicate an architect placeholder rather
// than a real approach description.
var ambiguousPlaceholders = []string{
	"tbd", "todo", "n/a", "see issue", "fix it", "implement it",
	"as needed", "per spec", "standard approach",
}

// ScoreSpec evaluates an architect's output spec and returns a quality verdict.
// A spec must pass all four required-field checks to be Ready:
//  1. At least one acceptance criterion
//  2. At least one file listed in files_touched
//  3. A non-zero blast radius estimate
//  4. A non-empty, non-placeholder approach description
//
// Score < 0.7 → send back to stage:architect with Feedback.
// Score ≥ 0.7 → advance to stage:implement.
func ScoreSpec(spec ArchitectSpec) SpecQualityResult {
	score := 1.0
	var missing []string

	if len(spec.AcceptanceCriteria) == 0 {
		score -= 0.30
		missing = append(missing, "acceptance_criteria")
	}

	if len(spec.FilesTouched) == 0 {
		score -= 0.25
		missing = append(missing, "files_touched")
	}

	if spec.BlastRadiusEstimate == 0 {
		score -= 0.20
		missing = append(missing, "blast_radius_estimate")
	}

	if isAmbiguousApproach(spec.Approach) {
		score -= 0.25
		missing = append(missing, "approach")
	}

	ready := len(missing) == 0 && score >= 0.7
	feedback := buildFeedback(spec.Title, missing)

	return SpecQualityResult{
		Ready:    ready,
		Score:    score,
		Missing:  missing,
		Feedback: feedback,
	}
}

// isAmbiguousApproach returns true when the approach is empty or contains only
// generic placeholder text that provides no real implementation guidance.
func isAmbiguousApproach(approach string) bool {
	trimmed := strings.TrimSpace(approach)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, placeholder := range ambiguousPlaceholders {
		if strings.Contains(lower, placeholder) {
			return true
		}
	}
	// Very short approaches (< 20 chars) are likely placeholders
	return len(trimmed) < 20
}

func buildFeedback(title string, missing []string) string {
	if len(missing) == 0 {
		return "spec is complete — ready for stage:implement"
	}
	var b strings.Builder
	b.WriteString("spec for ")
	if title != "" {
		b.WriteString("\"")
		b.WriteString(title)
		b.WriteString("\" ")
	}
	b.WriteString("is incomplete — missing: ")
	b.WriteString(strings.Join(missing, ", "))
	b.WriteString(". Please add: ")
	for i, field := range missing {
		switch field {
		case "acceptance_criteria":
			b.WriteString("at least one acceptance criterion")
		case "files_touched":
			b.WriteString("list of files the implementor should modify")
		case "blast_radius_estimate":
			b.WriteString("estimated number of files changed")
		case "approach":
			b.WriteString("a specific implementation approach (not a placeholder)")
		}
		if i < len(missing)-1 {
			b.WriteString("; ")
		}
	}
	return b.String()
}
