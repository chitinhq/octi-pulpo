package dispatch

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CompletionAction defines what agents to dispatch when an agent completes.
type CompletionAction struct {
	OnSuccess []string `json:"on_success"` // agents to dispatch on exit 0
	OnFailure []string `json:"on_failure"` // agents to dispatch on exit != 0
	OnCommit  []string `json:"on_commit"`  // agents to dispatch if the agent made git commits
}

// ChainConfig maps agent names to their completion actions.
type ChainConfig map[string]CompletionAction

// DefaultChains returns the standard completion chain configuration
// encoding the natural agent workflow:
//
//	SR finishes coding -> QA reviews
//	QA passes -> PR review
//	PR review done -> merger
//	Conductor/Director -> broadcast to EMs
func DefaultChains() ChainConfig {
	return ChainConfig{
		// SR finishes coding -> QA reviews, triage on failure
		"kernel-sr":     {OnCommit: []string{"kernel-qa"}, OnFailure: []string{"triage-failing-ci-agent"}},
		"cloud-sr":      {OnCommit: []string{"cloud-qa"}, OnFailure: []string{"ci-triage-agent-cloud"}},
		"shellforge-sr": {OnCommit: []string{"shellforge-qa"}},
		"octi-pulpo-sr": {OnCommit: []string{"octi-pulpo-qa"}},
		"studio-sr":     {OnCommit: []string{"studio-qa"}},
		"office-sim-sr": {OnCommit: []string{"office-sim-qa"}},

		// QA passes -> PR review
		"kernel-qa":    {OnSuccess: []string{"workspace-pr-review-agent"}},
		"cloud-qa":     {OnSuccess: []string{"code-review-agent-cloud"}},
		"shellforge-qa": {OnSuccess: []string{"shellforge-reviewer"}},

		// PR review done -> merger
		"workspace-pr-review-agent": {OnSuccess: []string{"pr-merger-agent"}},
		"code-review-agent-cloud":   {OnSuccess: []string{"pr-merger-agent-cloud"}},

		// EM reports -> director reads
		"kernel-em": {OnSuccess: []string{"hq-em"}},
		"cloud-em":  {OnSuccess: []string{"hq-em"}},

		// Conductor finishes -> dispatch EMs
		"jared-conductor": {OnSuccess: []string{"kernel-em", "cloud-em", "shellforge-em", "octi-pulpo-em", "studio-em"}},

		// Director finishes -> broadcast to all EMs
		"director": {OnSuccess: []string{"kernel-em", "cloud-em", "shellforge-em", "octi-pulpo-em", "studio-em", "marketing-em", "design-em", "site-em", "qa-em"}},
	}
}

// Targets returns the list of agents to dispatch based on exit code and commit status.
// Deduplicates in case OnSuccess and OnCommit overlap.
func (ca CompletionAction) Targets(exitCode int, madeCommits bool) []string {
	seen := make(map[string]bool)
	var targets []string

	add := func(agents []string) {
		for _, a := range agents {
			if !seen[a] {
				seen[a] = true
				targets = append(targets, a)
			}
		}
	}

	if exitCode == 0 {
		add(ca.OnSuccess)
	} else {
		add(ca.OnFailure)
	}
	if madeCommits {
		add(ca.OnCommit)
	}

	return targets
}

// TriggerChains checks the chain config for the completed agent and dispatches
// follow-up agents through the dispatcher.
func TriggerChains(ctx context.Context, d *Dispatcher, chains ChainConfig, agent string, exitCode int, madeCommits bool) []DispatchResult {
	action, ok := chains[agent]
	if !ok {
		return nil
	}

	targets := action.Targets(exitCode, madeCommits)
	if len(targets) == 0 {
		return nil
	}

	var results []DispatchResult
	for _, target := range targets {
		event := Event{
			Type:   EventCompletion,
			Source: agent,
			Payload: map[string]string{
				"trigger_agent": agent,
				"exit_code":     fmt.Sprintf("%d", exitCode),
				"had_commits":   fmt.Sprintf("%t", madeCommits),
			},
			Priority: 1, // completion chains are high priority
		}

		result, err := d.Dispatch(ctx, event, target, 1)
		if err != nil {
			results = append(results, DispatchResult{
				Agent:     target,
				Action:    "error",
				Reason:    fmt.Sprintf("chain dispatch error: %v", err),
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			continue
		}
		results = append(results, result)
	}

	return results
}

// CheckForCommits inspects the agent's most recent log file to determine
// whether it pushed any git commits during execution.
// It searches for push indicators like "Pushing branch" or "git push" output.
func CheckForCommits(agent, workspaceDir string) bool {
	// Common log locations (in order of preference)
	logPaths := []string{
		filepath.Join(workspaceDir, "server", "logs", agent+".log"),
		filepath.Join(workspaceDir, "logs", agent+".log"),
	}

	// Also check the most recent log file matching the agent name
	logDir := filepath.Join(workspaceDir, "server", "logs")
	entries, err := os.ReadDir(logDir)
	if err == nil {
		// Find most recent log file for this agent
		var newest string
		var newestTime time.Time
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), agent) && strings.HasSuffix(e.Name(), ".log") {
				info, err := e.Info()
				if err == nil && info.ModTime().After(newestTime) {
					newest = filepath.Join(logDir, e.Name())
					newestTime = info.ModTime()
				}
			}
		}
		if newest != "" {
			logPaths = append([]string{newest}, logPaths...)
		}
	}

	// Search patterns that indicate commits were made and pushed
	pushIndicators := []string{
		"Pushing branch",
		"To github.com:",
		"To git@github.com:",
		"remote: Create a pull request",
		"branch '->' 'origin/",
		"[new branch]",
		"git push",
	}

	for _, logPath := range logPaths {
		f, err := os.Open(logPath)
		if err != nil {
			continue
		}

		// Only scan last 200 lines (tail of log)
		scanner := bufio.NewScanner(f)
		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		f.Close()

		start := 0
		if len(lines) > 200 {
			start = len(lines) - 200
		}

		for _, line := range lines[start:] {
			for _, indicator := range pushIndicators {
				if strings.Contains(line, indicator) {
					return true
				}
			}
		}
	}

	return false
}
