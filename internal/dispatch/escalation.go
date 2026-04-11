package dispatch

const maxTotalAttempts = 4

// EscalationDecision tells the dispatcher what to do after a failure.
type EscalationDecision struct {
	Action   string // "retry-same-platform", "cross-platform", "human"
	Model    string // new model for retry (empty if human)
	Platform string // target platform for cross-platform (empty otherwise)
}

// EscalationManager handles model escalation within and across platforms.
type EscalationManager struct {
	modelRouter *ModelRouter
}

func NewEscalationManager(mr *ModelRouter) *EscalationManager {
	return &EscalationManager{modelRouter: mr}
}

func (em *EscalationManager) Escalate(platform, currentModel string, totalAttempts int) EscalationDecision {
	if totalAttempts >= maxTotalAttempts {
		return EscalationDecision{Action: "human"}
	}

	switch platform {
	case "copilot-cli":
		if next, ok := em.modelRouter.EscalateCopilot(currentModel); ok {
			return EscalationDecision{Action: "retry-same-platform", Model: next}
		}
		return EscalationDecision{Action: "cross-platform", Platform: "claude-code"}

	case "claude-code":
		if next, ok := em.modelRouter.EscalateClaude(currentModel); ok {
			return EscalationDecision{Action: "retry-same-platform", Model: next}
		}
		return EscalationDecision{Action: "human"}

	default:
		return EscalationDecision{Action: "human"}
	}
}
