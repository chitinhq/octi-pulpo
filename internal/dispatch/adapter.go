package dispatch

import "context"

// Adapter dispatches tasks to an execution surface.
type Adapter interface {
	Dispatch(ctx context.Context, task *Task) (*AdapterResult, error)
	CanAccept(task *Task) bool
	Name() string
}

// Task is a unit of work to dispatch to an execution surface.
type Task struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`     // "code-gen", "bugfix", "pr-review", "qa", "triage"
	Repo     string   `json:"repo"`     // "AgentGuardHQ/octi-pulpo"
	Prompt   string   `json:"prompt"`
	Toolset  []string `json:"toolset"`  // allowed tools for this task type
	Priority string   `json:"priority"` // "critical", "high", "normal", "background"
	Budget   int      `json:"budget"`   // max cost in cents
	Context  string   `json:"context"`  // pre-assembled context
	System   string   `json:"system"`   // system prompt
}

// AdapterResult is the outcome of a dispatched task.
type AdapterResult struct {
	TaskID    string  `json:"task_id"`
	Status    string  `json:"status"`     // "completed", "failed", "queued", "denied"
	Output    string  `json:"output"`
	CostCents int     `json:"cost_cents"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	Adapter   string  `json:"adapter"`
	Error     string  `json:"error,omitempty"`
	Quality   float64 `json:"quality,omitempty"`   // 0.0–1.0 output quality score
	Escalated bool    `json:"escalated,omitempty"` // true if retried at a higher tier
}
