package dispatch

import (
	"testing"
)

func TestDeepSeekAdapterName(t *testing.T) {
	a := NewDeepSeekAdapter("", "")
	if got := a.Name(); got != "deepseek" {
		t.Errorf("Name(): want deepseek, got %s", got)
	}
}

func TestDeepSeekAdapterCanAcceptTriage(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewDeepSeekAdapter("", "")
	task := &Task{Type: "triage"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(triage) with key set: want true, got false")
	}
}

func TestDeepSeekAdapterCanAcceptPRReview(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewDeepSeekAdapter("", "")
	task := &Task{Type: "pr-review"}
	if !a.CanAccept(task) {
		t.Error("CanAccept(pr-review) with key set: want true, got false")
	}
}

func TestDeepSeekAdapterRejectsCodeGen(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := NewDeepSeekAdapter("", "")
	task := &Task{Type: "code-gen"}
	if a.CanAccept(task) {
		t.Error("CanAccept(code-gen): want false, got true")
	}
}

func TestDeepSeekAdapterRejectsWithoutKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	a := NewDeepSeekAdapter("", "")
	task := &Task{Type: "triage"}
	if a.CanAccept(task) {
		t.Error("CanAccept(triage) without key: want false, got true")
	}
}

func TestDeepSeekAdapterDefaultModel(t *testing.T) {
	a := NewDeepSeekAdapter("", "")
	if a.model != defaultDeepSeekModel {
		t.Errorf("default model: want %s, got %s", defaultDeepSeekModel, a.model)
	}
}
