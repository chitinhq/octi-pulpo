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
// Tier 0 is DeepSeek for cheap triage/PR-review; tiers 1-3 are Anthropic.
var DefaultCascade = []ModelTier{
	{Model: "deepseek-coder", CostPerMTok: 0.14, Provider: "deepseek"},
	{Model: "claude-haiku-4-5-20251001", CostPerMTok: 0.80, Provider: "anthropic"},
	{Model: "claude-sonnet-4-6-20260320", CostPerMTok: 3.0, Provider: "anthropic"},
	{Model: "claude-opus-4-6-20260320", CostPerMTok: 15.0, Provider: "anthropic"},
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

	// Cap at max tier
	if score > len(DefaultCascade)-1 {
		score = len(DefaultCascade) - 1
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

// Dispatch selects the appropriate model tier based on task complexity
// and dispatches via the appropriate provider adapter.
func (c *CascadingAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
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
