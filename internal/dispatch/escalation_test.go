package dispatch

import "testing"

func TestEscalation_WithinPlatform(t *testing.T) {
	mr := NewModelRouter()
	em := NewEscalationManager(mr)

	decision := em.Escalate("copilot-cli", "gpt-5.4-nano", 1)
	if decision.Action != "retry-same-platform" || decision.Model != "gpt-5.4-mini" {
		t.Errorf("got action=%q model=%q, want retry-same-platform/gpt-5.4-mini", decision.Action, decision.Model)
	}

	decision = em.Escalate("copilot-cli", "gpt-5.4", 1)
	if decision.Action != "cross-platform" {
		t.Errorf("got action=%q, want cross-platform", decision.Action)
	}

	decision = em.Escalate("claude-code", "sonnet", 1)
	if decision.Action != "retry-same-platform" || decision.Model != "opus" {
		t.Errorf("got action=%q model=%q, want retry-same-platform/opus", decision.Action, decision.Model)
	}

	decision = em.Escalate("claude-code", "opus", 1)
	if decision.Action != "human" {
		t.Errorf("got action=%q, want human", decision.Action)
	}
}

func TestEscalation_TooManyAttempts(t *testing.T) {
	mr := NewModelRouter()
	em := NewEscalationManager(mr)

	decision := em.Escalate("copilot-cli", "gpt-5.4-nano", 4)
	if decision.Action != "human" {
		t.Errorf("got action=%q, want human after 4 attempts", decision.Action)
	}
}
