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
	// All signals fire: bugfix=1 + critical=1 + keyword=1 + long=1 = 4, capped to 2
	if got := TaskComplexity(task); got != 2 {
		t.Errorf("max signals: expected 2 (capped), got %d", got)
	}
}

func TestCascadingAdapterName(t *testing.T) {
	c := NewCascadingAdapter("")
	if c.Name() != "anthropic-cascade" {
		t.Errorf("expected anthropic-cascade, got %s", c.Name())
	}
}

func TestDefaultCascadeOrder(t *testing.T) {
	if len(DefaultCascade) != 3 {
		t.Fatalf("expected 3 tiers, got %d", len(DefaultCascade))
	}
	if DefaultCascade[0].CostPerMTok >= DefaultCascade[1].CostPerMTok {
		t.Error("tier 0 should be cheaper than tier 1")
	}
	if DefaultCascade[1].CostPerMTok >= DefaultCascade[2].CostPerMTok {
		t.Error("tier 1 should be cheaper than tier 2")
	}
}
