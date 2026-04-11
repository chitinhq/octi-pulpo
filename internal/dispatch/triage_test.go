package dispatch

import "testing"

// TestTriageResult_HasComplexity verifies the Complexity field exists on TriageResult.
func TestTriageResult_HasComplexity(t *testing.T) {
	r := TriageResult{
		Tier:       "tier:c",
		Complexity: "complexity:low",
		Reason:     "well scoped",
		Confidence: 0.9,
	}
	if r.Complexity == "" {
		t.Fatal("expected Complexity field to be set, got empty string")
	}
	if r.Complexity != "complexity:low" {
		t.Fatalf("expected complexity:low, got %q", r.Complexity)
	}
}

// TestInferComplexity covers the inference rules for all tier/title combinations.
func TestInferComplexity(t *testing.T) {
	cases := []struct {
		name  string
		tier  string
		title string
		want  string
	}{
		{
			name:  "tier:a-groom always high",
			tier:  "tier:a-groom",
			title: "some architectural decision",
			want:  "complexity:high",
		},
		{
			name:  "tier:c always low",
			tier:  "tier:c",
			title: "feat: add button color",
			want:  "complexity:low",
		},
		{
			name:  "tier:b-scope test: prefix → low",
			tier:  "tier:b-scope",
			title: "test: add unit tests for auth",
			want:  "complexity:low",
		},
		{
			name:  "tier:b-scope chore: prefix → low",
			tier:  "tier:b-scope",
			title: "chore: upgrade deps",
			want:  "complexity:low",
		},
		{
			name:  "tier:b-scope docs: prefix → low",
			tier:  "tier:b-scope",
			title: "docs: update README",
			want:  "complexity:low",
		},
		{
			name:  "tier:b-scope fix: prefix → high",
			tier:  "tier:b-scope",
			title: "fix: nil pointer in dispatch",
			want:  "complexity:high",
		},
		{
			name:  "tier:b-scope race condition → high",
			tier:  "tier:b-scope",
			title: "investigate race condition in scheduler",
			want:  "complexity:high",
		},
		{
			name:  "tier:b-scope security keyword → high",
			tier:  "tier:b-scope",
			title: "security: review token handling",
			want:  "complexity:high",
		},
		{
			name:  "tier:b-scope feat: → med",
			tier:  "tier:b-scope",
			title: "feat: add new dispatch route",
			want:  "complexity:med",
		},
		{
			name:  "unknown tier defaults to med",
			tier:  "tier:unknown",
			title: "something random",
			want:  "complexity:med",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inferComplexity(tc.tier, tc.title)
			if got != tc.want {
				t.Errorf("inferComplexity(%q, %q) = %q, want %q", tc.tier, tc.title, got, tc.want)
			}
		})
	}
}
