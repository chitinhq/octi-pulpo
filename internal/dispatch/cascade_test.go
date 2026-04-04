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
	// bugfix=1 + critical=1 = 2 raw, but capped at 1 (Haiku ceiling)
	if got := TaskComplexity(task); got != 1 {
		t.Errorf("bugfix+critical: expected 1 (capped at Haiku), got %d", got)
	}
}

func TestTaskComplexity_ArchitectureKeyword(t *testing.T) {
	task := &Task{Type: "code-gen", Priority: "normal", Prompt: "architect a new service layer"}
	// code-gen=1 + keyword=1 = 2 raw, capped at 1 (Haiku ceiling)
	if got := TaskComplexity(task); got != 1 {
		t.Errorf("code-gen+architect keyword: expected 1 (capped), got %d", got)
	}
}

func TestTaskComplexity_LongPrompt(t *testing.T) {
	longPrompt := make([]byte, 2500)
	for i := range longPrompt {
		longPrompt[i] = 'a'
	}
	task := &Task{Type: "triage", Priority: "normal", Prompt: string(longPrompt)}
	// triage=0 + long=1 = 1 (Haiku)
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
	// All signals fire: bugfix=1 + critical=1 + keyword=1 + long=1 = 4 raw
	// Capped to 1 (cascade only has 2 tiers: DeepSeek + Haiku)
	if got := TaskComplexity(task); got != 1 {
		t.Errorf("max signals: expected 1 (capped at Haiku), got %d", got)
	}
}

func TestNeedsEscalation_SimpleTask(t *testing.T) {
	task := &Task{Type: "triage", Priority: "normal", Prompt: "classify this"}
	if NeedsEscalation(task) {
		t.Error("triage should not need escalation")
	}
}

func TestNeedsEscalation_ComplexTask(t *testing.T) {
	task := &Task{
		Type:     "bugfix",
		Priority: "critical",
		Prompt:   "architect a major security refactor with " + string(make([]byte, 3000)),
	}
	if !NeedsEscalation(task) {
		t.Error("complex critical task should need escalation")
	}
}

func TestNeedsEscalation_CodeGen(t *testing.T) {
	task := &Task{Type: "code-gen", Priority: "normal", Prompt: "add a function"}
	// raw score = 1, cascade has 2 tiers (0,1), so 1 <= 1 — no escalation
	if NeedsEscalation(task) {
		t.Error("simple code-gen should not need escalation")
	}
}

func TestCascadingAdapterName(t *testing.T) {
	c := NewCascadingAdapter("")
	if c.Name() != "anthropic-cascade" {
		t.Errorf("expected anthropic-cascade, got %s", c.Name())
	}
}

func TestDefaultCascadeOrder(t *testing.T) {
	if len(DefaultCascade) != 2 {
		t.Fatalf("expected 2 tiers (DeepSeek + Haiku), got %d", len(DefaultCascade))
	}
	if DefaultCascade[0].CostPerMTok >= DefaultCascade[1].CostPerMTok {
		t.Error("tier 0 should be cheaper than tier 1")
	}
}

func TestDefaultCascadeHas2Tiers(t *testing.T) {
	if len(DefaultCascade) != 2 {
		t.Fatalf("expected 2 tiers, got %d", len(DefaultCascade))
	}
	tier0 := DefaultCascade[0]
	if tier0.Provider != "deepseek" {
		t.Errorf("tier 0 provider: want deepseek, got %s", tier0.Provider)
	}
	tier1 := DefaultCascade[1]
	if tier1.Provider != "anthropic" {
		t.Errorf("tier 1 provider: want anthropic, got %s", tier1.Provider)
	}
	if tier1.Model != "claude-3-haiku-20241022" {
		t.Errorf("tier 1 model: want haiku, got %s", tier1.Model)
	}
}

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

func TestAdapterResultQualityFields(t *testing.T) {
	r := &AdapterResult{TaskID: "t1", Status: "completed"}
	if r.Quality != 0.0 {
		t.Errorf("Quality zero value: want 0.0, got %f", r.Quality)
	}
	if r.Escalated != false {
		t.Errorf("Escalated zero value: want false, got %v", r.Escalated)
	}
	r.Quality = 0.95
	r.Escalated = true
	if r.Quality != 0.95 {
		t.Errorf("Quality: want 0.95, got %f", r.Quality)
	}
	if !r.Escalated {
		t.Error("Escalated: want true, got false")
	}
}

func TestCascadeDispatchEscalates(t *testing.T) {
	c := NewCascadingAdapter("")
	task := &Task{
		Type:     "bugfix",
		Priority: "critical",
		Prompt:   "architect a major security refactor with " + string(make([]byte, 3000)),
	}
	result, err := c.Dispatch(nil, task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "escalated" {
		t.Errorf("expected escalated status, got %s", result.Status)
	}
	if !result.Escalated {
		t.Error("expected Escalated=true")
	}
}
