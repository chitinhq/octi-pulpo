package admission

import (
	"strings"
	"testing"
)

func TestScoreSpec_AllFieldsPresent(t *testing.T) {
	spec := ArchitectSpec{
		Title:               "Add rate limiting to API",
		AcceptanceCriteria:  []string{"requests over limit return 429", "headers include X-RateLimit-*"},
		FilesTouched:        []string{"internal/api/middleware.go", "internal/api/middleware_test.go"},
		BlastRadiusEstimate: 2,
		Approach:            "Implement a token-bucket middleware using Redis INCR with a 60s expiry key per user.",
	}
	result := ScoreSpec(spec)
	if !result.Ready {
		t.Errorf("expected Ready=true, got feedback: %s", result.Feedback)
	}
	if result.Score < 0.99 {
		t.Errorf("expected score ~1.0, got %.2f", result.Score)
	}
	if len(result.Missing) != 0 {
		t.Errorf("expected no missing fields, got %v", result.Missing)
	}
}

func TestScoreSpec_MissingAcceptanceCriteria(t *testing.T) {
	spec := ArchitectSpec{
		Title:               "Fix login bug",
		AcceptanceCriteria:  nil,
		FilesTouched:        []string{"internal/auth/login.go"},
		BlastRadiusEstimate: 1,
		Approach:            "Check the session token expiry logic in validateSession and extend the window.",
	}
	result := ScoreSpec(spec)
	if result.Ready {
		t.Error("expected Ready=false when acceptance_criteria is empty")
	}
	if !contains(result.Missing, "acceptance_criteria") {
		t.Errorf("expected 'acceptance_criteria' in Missing, got %v", result.Missing)
	}
}

func TestScoreSpec_MissingFilesTouched(t *testing.T) {
	spec := ArchitectSpec{
		Title:               "Refactor DB layer",
		AcceptanceCriteria:  []string{"all queries use prepared statements"},
		FilesTouched:        nil,
		BlastRadiusEstimate: 5,
		Approach:            "Replace raw sql.Query calls with sqlx.NamedQuery across the repository layer.",
	}
	result := ScoreSpec(spec)
	if result.Ready {
		t.Error("expected Ready=false when files_touched is empty")
	}
	if !contains(result.Missing, "files_touched") {
		t.Errorf("expected 'files_touched' in Missing, got %v", result.Missing)
	}
}

func TestScoreSpec_ZeroBlastRadius(t *testing.T) {
	spec := ArchitectSpec{
		Title:               "Update config",
		AcceptanceCriteria:  []string{"config loads correctly"},
		FilesTouched:        []string{"config/app.yaml"},
		BlastRadiusEstimate: 0,
		Approach:            "Add a new timeout_seconds field to the YAML config and wire it through AppConfig.",
	}
	result := ScoreSpec(spec)
	if result.Ready {
		t.Error("expected Ready=false when blast_radius_estimate is 0")
	}
	if !contains(result.Missing, "blast_radius_estimate") {
		t.Errorf("expected 'blast_radius_estimate' in Missing, got %v", result.Missing)
	}
}

func TestScoreSpec_AmbiguousApproach(t *testing.T) {
	tests := []struct {
		approach string
		desc     string
	}{
		{"", "empty"},
		{"TBD", "TBD placeholder"},
		{"fix it", "generic fix it"},
		{"TODO", "TODO placeholder"},
		{"short", "too short"},
		{"As needed", "as needed placeholder"},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			spec := ArchitectSpec{
				Title:               "Do something",
				AcceptanceCriteria:  []string{"it works"},
				FilesTouched:        []string{"main.go"},
				BlastRadiusEstimate: 1,
				Approach:            tc.approach,
			}
			result := ScoreSpec(spec)
			if result.Ready {
				t.Errorf("expected Ready=false for ambiguous approach %q", tc.approach)
			}
			if !contains(result.Missing, "approach") {
				t.Errorf("expected 'approach' in Missing for %q, got %v", tc.approach, result.Missing)
			}
		})
	}
}

func TestScoreSpec_FeedbackMessageWhenIncomplete(t *testing.T) {
	spec := ArchitectSpec{
		Title: "Add caching",
	}
	result := ScoreSpec(spec)
	if result.Ready {
		t.Error("expected Ready=false for empty spec")
	}
	if result.Feedback == "" {
		t.Error("expected non-empty feedback when spec is incomplete")
	}
	if !strings.Contains(result.Feedback, "acceptance_criteria") {
		t.Error("expected feedback to mention acceptance_criteria")
	}
}

func TestScoreSpec_FeedbackWhenComplete(t *testing.T) {
	spec := ArchitectSpec{
		Title:               "Migrate DB schema",
		AcceptanceCriteria:  []string{"migration runs idempotently", "rollback reverts all changes"},
		FilesTouched:        []string{"migrations/003_add_index.sql", "internal/db/migrate.go"},
		BlastRadiusEstimate: 2,
		Approach:            "Write an up/down migration for the new composite index; wire into the CLI migrate command.",
	}
	result := ScoreSpec(spec)
	if !result.Ready {
		t.Errorf("expected Ready=true, got: %s", result.Feedback)
	}
	if !strings.Contains(result.Feedback, "ready for stage:implement") {
		t.Errorf("expected success feedback, got: %s", result.Feedback)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
