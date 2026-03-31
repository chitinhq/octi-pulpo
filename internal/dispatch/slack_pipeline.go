package dispatch

import (
	"fmt"
	"strings"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/pipeline"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

// FormatPipelineDashboard builds Slack Block Kit blocks showing pipeline stage
// depths, session counts, driver budgets, and backpressure status.
func FormatPipelineDashboard(
	depths map[pipeline.Stage]int,
	sessions map[pipeline.Stage]int,
	budgets []routing.DriverHealth,
	bp pipeline.BackpressureAction,
) []map[string]interface{} {
	now := time.Now().UTC().Format("15:04 UTC")
	var blocks []map[string]interface{}

	blocks = append(blocks, slackSection(fmt.Sprintf("*Pipeline Status (%s)*", now)))

	stages := []pipeline.Stage{
		pipeline.StageArchitect, pipeline.StageImplement,
		pipeline.StageQA, pipeline.StageReview, pipeline.StageRelease,
	}

	var lines []string
	for _, s := range stages {
		icon := stageIcon(s, depths[s], bp)
		name := strings.ToUpper(string(s))
		lines = append(lines, fmt.Sprintf("%s *%s*: %d queued, %d sessions", icon, name, depths[s], sessions[s]))
	}
	blocks = append(blocks, slackSection(strings.Join(lines, "\n")))

	if bp.PauseStage != "" || bp.ThrottleStage != "" {
		blocks = append(blocks, slackSection(fmt.Sprintf("Warning: *Backpressure:* %s", bp.Reason)))
	}

	if len(budgets) > 0 {
		var budgetLines []string
		for _, b := range budgets {
			icon := "G"
			pct := "?"
			if b.BudgetPct != nil {
				pct = fmt.Sprintf("%d%%", *b.BudgetPct)
				if *b.BudgetPct < 20 {
					icon = "R"
				} else if *b.BudgetPct < 50 {
					icon = "Y"
				}
			}
			if b.CircuitState == "OPEN" {
				icon = "R"
			}
			budgetLines = append(budgetLines, fmt.Sprintf("%s *%s*: %s remaining", icon, b.Name, pct))
		}
		blocks = append(blocks, slackSection("*Driver Budgets*\n"+strings.Join(budgetLines, "\n")))
	}

	return blocks
}

// PipelineCommand represents a parsed Slack command for the pipeline.
type PipelineCommand struct {
	Action string
	Args   string
}

// ParsePipelineCommand extracts a pipeline command from a Slack message.
func ParsePipelineCommand(text string) (PipelineCommand, bool) {
	text = strings.TrimSpace(strings.ToLower(text))

	if !strings.HasPrefix(text, "pipeline") {
		return PipelineCommand{}, false
	}

	rest := strings.TrimSpace(text[len("pipeline"):])

	if rest == "" {
		return PipelineCommand{Action: "status"}, true
	}

	parts := strings.SplitN(rest, " ", 2)
	cmd := PipelineCommand{Action: parts[0]}
	if len(parts) > 1 {
		cmd.Args = parts[1]
	}

	switch cmd.Action {
	case "status", "pause", "resume", "prioritize", "kill":
		return cmd, true
	default:
		return PipelineCommand{}, false
	}
}

// FormatBudgetAlert creates a Slack message for low budget warnings.
func FormatBudgetAlert(driver string, budgetPct int, queuedArchitectTasks int) string {
	emoji := "Warning"
	if budgetPct < 10 {
		emoji = "CRITICAL"
	}

	msg := fmt.Sprintf("%s *Budget Alert:* %s at %d%% remaining", emoji, driver, budgetPct)
	if queuedArchitectTasks > 0 {
		msg += fmt.Sprintf(" — %d architect tasks queued waiting for budget", queuedArchitectTasks)
	}
	return msg
}

// FormatEscalation builds Slack Block Kit blocks for a high-risk PR escalation
// with approve/reject action buttons.
func FormatEscalation(repo string, prNumber int, reason string, riskScore int) []map[string]interface{} {
	var blocks []map[string]interface{}

	level := "Elevated"
	if riskScore > 60 {
		level = "CRITICAL"
	}

	blocks = append(blocks, slackSection(
		fmt.Sprintf("%s *Escalation: %s#%d*\nRisk score: %d\nReason: %s",
			level, repo, prNumber, riskScore, reason),
	))

	blocks = append(blocks, map[string]interface{}{
		"type": "actions",
		"elements": []map[string]interface{}{
			{
				"type": "button",
				"text": map[string]interface{}{
					"type": "plain_text",
					"text": "Approve",
				},
				"style":     "primary",
				"action_id": fmt.Sprintf("escalation_approve_%s_%d", repo, prNumber),
			},
			{
				"type": "button",
				"text": map[string]interface{}{
					"type": "plain_text",
					"text": "Reject",
				},
				"style":     "danger",
				"action_id": fmt.Sprintf("escalation_reject_%s_%d", repo, prNumber),
			},
			{
				"type": "button",
				"text": map[string]interface{}{
					"type": "plain_text",
					"text": "View PR",
				},
				"url": fmt.Sprintf("https://github.com/%s/pull/%d", repo, prNumber),
			},
		},
	})

	return blocks
}

func stageIcon(s pipeline.Stage, depth int, bp pipeline.BackpressureAction) string {
	if bp.PauseStage == s {
		return "PAUSED"
	}
	if bp.ThrottleStage == s {
		return "SLOW"
	}
	if depth == 0 {
		return "o"
	}
	if depth > 8 {
		return "!"
	}
	if depth > 4 {
		return "~"
	}
	return "+"
}
