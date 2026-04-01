// Package learner closes the feedback loop: task outcomes are automatically
// stored as episodic memories and recalled to improve future dispatches.
package learner

import (
	"context"
	"fmt"
	"strings"

	"github.com/AgentGuardHQ/octi-pulpo/internal/memory"
)

// TaskInfo is the minimal task data needed for learning (avoids import cycle with dispatch).
type TaskInfo struct {
	Type     string
	Repo     string
	Prompt   string
	Priority string
}

// OutcomeInfo is the minimal result data needed for learning.
type OutcomeInfo struct {
	Status    string
	Adapter   string
	TokensIn  int
	TokensOut int
	CostCents int
	Output    string
	Error     string
}

// Learner stores task outcomes and recalls relevant prior experience.
type Learner struct {
	mem *memory.Store
}

// New creates a Learner backed by the given memory store.
func New(mem *memory.Store) *Learner {
	return &Learner{mem: mem}
}

// RecordOutcome stores a completed task's outcome as an episodic memory.
// Called automatically after every dispatch completes.
func (l *Learner) RecordOutcome(ctx context.Context, task *TaskInfo, result *OutcomeInfo) error {
	// Build a rich episodic memory from the task + result.
	var parts []string
	parts = append(parts, fmt.Sprintf("Task: %s", task.Prompt))
	parts = append(parts, fmt.Sprintf("Type: %s | Repo: %s | Priority: %s", task.Type, task.Repo, task.Priority))
	parts = append(parts, fmt.Sprintf("Outcome: %s | Adapter: %s", result.Status, result.Adapter))

	if result.TokensIn > 0 || result.TokensOut > 0 {
		parts = append(parts, fmt.Sprintf("Tokens: %d in / %d out | Cost: $%.4f",
			result.TokensIn, result.TokensOut, float64(result.CostCents)/100))
	}

	if result.Error != "" {
		parts = append(parts, fmt.Sprintf("Error: %s", result.Error))
	}

	// Extract a useful summary from the output (first 500 chars).
	if result.Output != "" {
		summary := result.Output
		if len(summary) > 500 {
			summary = summary[:500] + "..."
		}
		parts = append(parts, fmt.Sprintf("Summary: %s", summary))
	}

	content := strings.Join(parts, "\n")

	// Build topics for keyword search + filtering.
	topics := []string{
		"task-outcome",
		task.Type,
		result.Status,
	}
	if task.Repo != "" {
		topics = append(topics, repoShortName(task.Repo))
	}

	agentID := "octi-pulpo:learner"
	_, err := l.mem.Put(ctx, agentID, content, topics)
	return err
}

// RecallSimilar searches episodic memory for prior task outcomes similar
// to the given task. Returns formatted context to inject into the prompt.
// Returns empty string if no relevant memories found.
func (l *Learner) RecallSimilar(ctx context.Context, task *TaskInfo) string {
	query := fmt.Sprintf("%s %s %s", task.Type, task.Prompt, task.Repo)

	entries, err := l.mem.Recall(ctx, query, 3)
	if err != nil || len(entries) == 0 {
		return ""
	}

	var parts []string
	parts = append(parts, "## Prior Experience (from episodic memory)")
	parts = append(parts, "The following similar tasks have been completed before. Use these as reference:")
	parts = append(parts, "")

	for i, entry := range entries {
		parts = append(parts, fmt.Sprintf("### Prior task %d", i+1))
		parts = append(parts, entry.Content)
		parts = append(parts, "")
	}

	return strings.Join(parts, "\n")
}

func repoShortName(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) == 2 {
		return parts[1]
	}
	return repo
}
