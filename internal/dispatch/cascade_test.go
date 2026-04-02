package dispatch

import "testing"

func TestTaskComplexity_Triage(t *testing.T) {
	task := &Task{Type: "triage", Priority: "normal", Prompt: "classify this issue"}
	if got := TaskComplexity(task); got != 0 {
		t.Errorf("triage+normal: expected 0, got %d", got)
	}
}

func TestTaskComplexity_CodeGen(t *testing.T) {
	task := &Task{Type: "code-gen", Priority: "normal", Prompt: "add a function"}
	if got := TaskComplexity(task); got != 1 {
		t.Errorf("code-gen+normal: expected 1, got %d", got)
	}
}

func TestTaskComplexity_CriticalBugfix(t *testing.T) {
	task := &Task{Type: "bugfix", Priority: "critical", Prompt: "fix the crash"}
	// bugfix=1 + critical=1 = 2 (Opus)
	if got := TaskComplexity(task); got != 2 {
		t.Errorf("bugfix+critical: expected 2, got %d", got)
	}
}

func TestTaskComplexity_ArchitectureKeyword(t *testing.T) {
	task := &Task{Type: "code-gen", Priority: "normal", Prompt: "architect a new service layer"}
	// code-gen=1 + keyword=1 = 2 (Opus)
	if got := TaskComplexity(task); got != 2 {
		t.Errorf("code-gen+architect keyword: expected 2, got %d", got)
	}
}

func TestTaskComplexity_LongPrompt(t *testing.T) {
	longPrompt := make([]byte, 2500)
	for i := range longPrompt {
		longPrompt[i] = 'a'
	}
	task := &Task{Type: "triage", Priority: "normal", Prompt: string(longPrompt)}
	// triage=0 + long=1 = 1 (Sonnet)
	if got := TaskComplexity(task); got != 1 {
		t.Errorf("triage+long prompt: expected 1, got %d", got)
	}
}

func TestTaskComplexity_CapsAtMax(t *testing.T) {
	task := &Task{
		Type:     "bugfix",
		Priority: "critical",
		Prompt:   "architect a major security refactor with " + string(make([]byte, 3000)),
	}
	// All signals fire: bugfix=1 + critical=1 + keyword=1 + long=1 = 4, capped to 3 (4 tiers)
	if got := TaskComplexity(task); got != 3 {
		t.Errorf("max signals: expected 3 (capped), got %d", got)
	}
}

func TestCascadingAdapterName(t *testing.T) {
	c := NewCascadingAdapter("")
	if c.Name() != "anthropic-cascade" {
		t.Errorf("expected anthropic-cascade, got %s", c.Name())
	}
}

func TestDefaultCascadeOrder(t *testing.T) {
	if len(DefaultCascade) != 4 {
		t.Fatalf("expected 4 tiers, got %d", len(DefaultCascade))
	}
	for i := 1; i < len(DefaultCascade); i++ {
		if DefaultCascade[i-1].CostPerMTok >= DefaultCascade[i].CostPerMTok {
			t.Errorf("tier %d should be cheaper than tier %d", i-1, i)
		}
	}
}

// TestDefaultCascadeHas4Tiers verifies the cascade has exactly 4 tiers and
// that tier 0 is the DeepSeek model.
func TestDefaultCascadeHas4Tiers(t *testing.T) {
	if len(DefaultCascade) != 4 {
		t.Fatalf("expected 4 tiers, got %d", len(DefaultCascade))
	}
	tier0 := DefaultCascade[0]
	if tier0.Provider != "deepseek" {
		t.Errorf("tier 0 provider: want deepseek, got %s", tier0.Provider)
	}
	if tier0.Model != "deepseek-coder" {
		t.Errorf("tier 0 model: want deepseek-coder, got %s", tier0.Model)
	}
}

// TestTaskComplexityTriageIsDeepSeek verifies that a triage task gets
// complexity score 0, which maps to the DeepSeek tier.
func TestTaskComplexityTriageIsDeepSeek(t *testing.T) {
	task := &Task{
		ID:       "t1",
		Type:     "triage",
		Priority: "normal",
		Prompt:   "classify this issue",
	}
	score := TaskComplexity(task)
	if score != 0 {
		t.Fatalf("expected score 0 for triage task, got %d", score)
	}
	tier := DefaultCascade[score]
	if tier.Provider != "deepseek" {
		t.Errorf("expected tier 0 to be deepseek provider, got %s", tier.Provider)
	}
}

// TestAdapterResultQualityFields verifies that AdapterResult carries the new
// Quality and Escalated fields with their zero values.
func TestAdapterResultQualityFields(t *testing.T) {
	r := &AdapterResult{
		TaskID: "t1",
		Status: "completed",
	}
	if r.Quality != 0.0 {
		t.Errorf("Quality zero value: want 0.0, got %f", r.Quality)
	}
	if r.Escalated != false {
		t.Errorf("Escalated zero value: want false, got %v", r.Escalated)
	}

	// Verify the fields can be set and read back.
	r.Quality = 0.95
	r.Escalated = true
	if r.Quality != 0.95 {
		t.Errorf("Quality: want 0.95, got %f", r.Quality)
	}
	if !r.Escalated {
		t.Error("Escalated: want true, got false")
	}
}
