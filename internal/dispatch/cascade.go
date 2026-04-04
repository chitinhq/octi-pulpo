package dispatch

import (
	"context"
	"fmt"
	"strings"
)

// ModelTier defines cost/capability tiers for model cascading.
type ModelTier struct {
	Model       string  // Model ID
	CostPerMTok float64 // input cost per million tokens (for logging)
	Provider    string  // "anthropic" or "deepseek"
}

// DefaultCascade is the cost-ordered model cascade: cheapest first.
// Capped at Haiku — Sonnet/Opus work is escalated to Jared's Claude Code
// CLI session (Max subscription) instead of burning API budget.
var DefaultCascade = []ModelTier{
	{Model: "deepseek-coder", CostPerMTok: 0.14, Provider: "deepseek"},
	{Model: "claude-3-haiku-20241022", CostPerMTok: 0.80, Provider: "anthropic"},
	// Sonnet and Opus removed — escalate to human via GitHub issue instead.
	// See: feedback_model_budget.md
}

// TaskComplexity scores a task's complexity to determine model tier.
// Returns 0-2: 0=Haiku, 1=Sonnet, 2=Opus.
func TaskComplexity(task *Task) int {
	score := 0

	// Task type signals
	switch task.Type {
	case "triage", "pr-review":
		score = 0 // simple classification / review
	case "qa":
		score = 1 // needs reasoning about test coverage
	case "code-gen", "bugfix":
		score = 1 // needs code writing
	}

	// Priority signals — critical tasks get better models
	if task.Priority == "critical" {
		score++
	}

	// Prompt length as complexity proxy
	if len(task.Prompt) > 2000 {
		score++
	}

	// Keywords that signal complex reasoning
	complexKeywords := []string{
		"architect", "design", "refactor", "migration",
		"security", "vulnerability", "performance",
		"debug complex", "root cause",
	}
	promptLower := strings.ToLower(task.Prompt)
	for _, kw := range complexKeywords {
		if strings.Contains(promptLower, kw) {
			score++
			break
		}
	}

	// Cap at Haiku (tier 1) — anything above is escalated to human
	maxTier := len(DefaultCascade) - 1
	if score > maxTier {
		score = maxTier
	}

	return score
}

// NeedsEscalation returns true if the task complexity exceeds what
// the automated cascade can handle (i.e., would need Sonnet/Opus).
// These tasks should be filed as GitHub issues for Jared to handle
// in his Claude Code CLI session using the Max subscription.
func NeedsEscalation(task *Task) bool {
	return rawComplexityScore(task) > len(DefaultCascade)-1
}

// rawComplexityScore computes the uncapped complexity score.
func rawComplexityScore(task *Task) int {
	score := 0
	switch task.Type {
	case "triage", "pr-review":
		score = 0
	case "qa":
		score = 1
	case "code-gen", "bugfix":
		score = 1
	}
	if task.Priority == "critical" {
		score++
	}
	if len(task.Prompt) > 2000 {
		score++
	}
	complexKeywords := []string{
		"architect", "design", "refactor", "migration",
		"security", "vulnerability", "performance",
		"debug complex", "root cause",
	}
	promptLower := strings.ToLower(task.Prompt)
	for _, kw := range complexKeywords {
		if strings.Contains(promptLower, kw) {
			score++
			break
		}
	}
	return score
}

// CascadingAdapter wraps AnthropicAdapter with automatic model selection
// based on task complexity. Starts with the cheapest sufficient model.
type CascadingAdapter struct {
	shellforge string
	cascade    []ModelTier
}

// NewCascadingAdapter creates a CascadingAdapter with the default cascade.
func NewCascadingAdapter(shellforge string) *CascadingAdapter {
	if shellforge == "" {
		shellforge = defaultShellforge
	}
	return &CascadingAdapter{
		shellforge: shellforge,
		cascade:    DefaultCascade,
	}
}

// Name returns the adapter identifier.
func (c *CascadingAdapter) Name() string {
	return "anthropic-cascade"
}

// CanAccept delegates to AnthropicAdapter.CanAccept.
func (c *CascadingAdapter) CanAccept(task *Task) bool {
	return NewAnthropicAdapter(c.shellforge, "").CanAccept(task)
}

// Dispatch selects the appropriate model tier based on task complexity.
// If the task exceeds the cascade ceiling (needs Sonnet/Opus), it returns
// an "escalated" result instead of burning API budget — the caller should
// file a GitHub issue for human + Claude Code CLI handling.
func (c *CascadingAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
	if NeedsEscalation(task) {
		return &AdapterResult{
			TaskID:    task.ID,
			Adapter:   c.Name(),
			Status:    "escalated",
			Escalated: true,
			Error:     "Task complexity exceeds API budget ceiling. Escalate to Jared via Claude Code CLI (Max subscription).",
		}, nil
	}

	tier := TaskComplexity(task)
	if tier >= len(c.cascade) {
		tier = len(c.cascade) - 1
	}

	selected := c.cascade[tier]

	var adapter Adapter
	if selected.Provider == "deepseek" {
		adapter = NewDeepSeekAdapter(c.shellforge, selected.Model)
	} else {
		adapter = NewAnthropicAdapter(c.shellforge, selected.Model)
	}

	result, err := adapter.Dispatch(ctx, task)
	if result != nil {
		result.Adapter = fmt.Sprintf("%s-cascade:%s", selected.Provider, selected.Model)
	}
	return result, err
}
