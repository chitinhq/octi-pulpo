package learner

import (
	"context"
	"fmt"
	"strings"

	"github.com/AgentGuardHQ/octi-pulpo/internal/memory"
)

// Procedure is a learned recipe extracted from episodic memories.
type Procedure struct {
	Pattern     string   // task type + repo pattern this applies to
	Recipe      string   // step-by-step approach
	AvgTurns    int      // average turns for successful completions
	SuccessRate float64  // fraction of successes (0.0–1.0)
	TimesUsed   int      // how many episodes contributed
	Topics      []string // searchable topics
}

// ProcedureExtractor analyzes episodic memories and promotes patterns
// to procedural recipes.
type ProcedureExtractor struct {
	mem *memory.Store
}

// NewProcedureExtractor creates an extractor backed by the memory store.
func NewProcedureExtractor(mem *memory.Store) *ProcedureExtractor {
	return &ProcedureExtractor{mem: mem}
}

// Extract reads recent episodic memories, clusters by task type + repo,
// and returns procedural recipes for patterns with enough data.
// minEpisodes is the minimum number of episodes needed to form a procedure.
func (pe *ProcedureExtractor) Extract(ctx context.Context, minEpisodes int) ([]Procedure, error) {
	if minEpisodes < 2 {
		minEpisodes = 2
	}

	// Recall all task-outcome episodes.
	entries, err := pe.mem.Recall(ctx, "task-outcome", 100)
	if err != nil {
		return nil, fmt.Errorf("recall episodes: %w", err)
	}

	// Cluster by pattern key (type + repo).
	clusters := make(map[string][]episodeData)
	for _, entry := range entries {
		ep := parseEpisode(entry.Content)
		if ep.taskType == "" {
			continue
		}
		key := ep.taskType + ":" + ep.repo
		clusters[key] = append(clusters[key], ep)
	}

	// Build procedures from clusters with enough data.
	var procedures []Procedure
	for pattern, episodes := range clusters {
		if len(episodes) < minEpisodes {
			continue
		}

		proc := buildProcedure(pattern, episodes)
		procedures = append(procedures, proc)
	}

	return procedures, nil
}

// Store saves extracted procedures back to memory as procedural entries.
func (pe *ProcedureExtractor) Store(ctx context.Context, procedures []Procedure) error {
	for _, proc := range procedures {
		content := fmt.Sprintf("PROCEDURE: %s\n\nRecipe: %s\n\nStats: %d episodes, %.0f%% success rate, ~%d avg turns",
			proc.Pattern, proc.Recipe, proc.TimesUsed, proc.SuccessRate*100, proc.AvgTurns)

		topics := append([]string{"procedure", proc.Pattern}, proc.Topics...)
		_, err := pe.mem.Put(ctx, "octi-pulpo:procedure-extractor", content, topics)
		if err != nil {
			return fmt.Errorf("store procedure %s: %w", proc.Pattern, err)
		}
	}
	return nil
}

// episodeData is parsed from an episodic memory's content.
type episodeData struct {
	taskType string
	repo     string
	status   string
	prompt   string
}

// parseEpisode extracts structured data from an episodic memory's content string.
func parseEpisode(content string) episodeData {
	var ep episodeData
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Type:") {
			// Format: "Type: bugfix | Repo: AgentGuardHQ/octi-pulpo | Priority: high"
			parts := strings.Split(line, "|")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if strings.HasPrefix(p, "Type:") {
					ep.taskType = strings.TrimSpace(strings.TrimPrefix(p, "Type:"))
				} else if strings.HasPrefix(p, "Repo:") {
					ep.repo = strings.TrimSpace(strings.TrimPrefix(p, "Repo:"))
				}
			}
		} else if strings.HasPrefix(line, "Outcome:") {
			parts := strings.Split(line, "|")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if strings.HasPrefix(p, "Outcome:") {
					ep.status = strings.TrimSpace(strings.TrimPrefix(p, "Outcome:"))
				}
			}
		} else if strings.HasPrefix(line, "Task:") {
			ep.prompt = strings.TrimSpace(strings.TrimPrefix(line, "Task:"))
		}
	}
	return ep
}

// buildProcedure synthesizes a procedure from a cluster of episodes.
func buildProcedure(pattern string, episodes []episodeData) Procedure {
	var successes int
	for _, ep := range episodes {
		if ep.status == "completed" {
			successes++
		}
	}

	// Collect unique task prompts for the recipe.
	promptSet := make(map[string]bool)
	for _, ep := range episodes {
		if ep.prompt != "" {
			promptSet[ep.prompt] = true
		}
	}
	var prompts []string
	for p := range promptSet {
		prompts = append(prompts, "- "+p)
	}

	recipe := fmt.Sprintf("Pattern: %s\nSimilar tasks seen:\n%s", pattern, strings.Join(prompts, "\n"))

	parts := strings.SplitN(pattern, ":", 2)
	topics := []string{}
	if len(parts) == 2 {
		topics = append(topics, parts[0]) // task type
		if parts[1] != "" {
			topics = append(topics, repoShortName(parts[1]))
		}
	}

	return Procedure{
		Pattern:     pattern,
		Recipe:      recipe,
		SuccessRate: float64(successes) / float64(len(episodes)),
		TimesUsed:   len(episodes),
		Topics:      topics,
	}
}
