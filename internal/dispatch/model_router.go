package dispatch

// ModelRouter maps complexity labels to model identifiers per platform.
type ModelRouter struct {
	copilotModels map[string]string
	claudeModels  map[string]string
	copilotLadder map[string]string // current → next for escalation
	claudeLadder  map[string]string
}

func NewModelRouter() *ModelRouter {
	return &ModelRouter{
		copilotModels: map[string]string{
			"low":  "gpt-5.4-nano",
			"med":  "gpt-5.4-mini",
			"high": "gpt-5.4",
		},
		claudeModels: map[string]string{
			"low":  "sonnet",
			"med":  "sonnet",
			"high": "opus",
		},
		copilotLadder: map[string]string{
			"gpt-5.4-nano": "gpt-5.4-mini",
			"gpt-5.4-mini": "gpt-5.4",
		},
		claudeLadder: map[string]string{
			"sonnet": "opus",
		},
	}
}

func (r *ModelRouter) CopilotModel(complexity string) string {
	if m, ok := r.copilotModels[complexity]; ok {
		return m
	}
	return r.copilotModels["low"]
}

func (r *ModelRouter) ClaudeModel(complexity string) string {
	if m, ok := r.claudeModels[complexity]; ok {
		return m
	}
	return r.claudeModels["low"]
}

func (r *ModelRouter) EscalateCopilot(current string) (string, bool) {
	next, ok := r.copilotLadder[current]
	return next, ok
}

func (r *ModelRouter) EscalateClaude(current string) (string, bool) {
	next, ok := r.claudeLadder[current]
	return next, ok
}
