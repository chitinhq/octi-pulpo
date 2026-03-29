package dispatch

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// LeaderboardEntry represents one agent's ranked productivity data.
type LeaderboardEntry struct {
	Rank        int     `json:"rank"`
	Agent       string  `json:"agent"`
	Score       float64 `json:"score"`
	Verdict     string  `json:"verdict"` // promote, retain, monitor, fire
	AvgCommits  float64 `json:"avg_commits"`
	FailRate    float64 `json:"fail_rate"`
	AvgDuration float64 `json:"avg_duration_s"`
	ConsecFails int     `json:"consec_fails"`
	TriageFlag  bool    `json:"triage_flag"`
	RunCount    int     `json:"run_count"`
}

// Score computes a productivity score for an AgentProfile.
// Higher is better; negative scores indicate a net drain on the swarm.
//
// Formula:
//
//	output      = avgCommits × 5.0            (commit output quality)
//	reliability = (1 − failRate) × 3.0        (execution reliability)
//	effort      = +1.0 if avgDuration > 30s,
//	              −0.5 if avgDuration < 10s    (sustained effort vs idle)
//	triagePen   = −10.0 if triageFlag          (needs human review)
//	failPen     = consecutiveFails × −0.5      (recent failure streak)
//
//	score = output + reliability + effort + triagePen + failPen
func Score(p AgentProfile) float64 {
	if len(p.RecentResults) == 0 {
		return 0
	}

	output := p.AvgCommits * 5.0
	reliability := (1.0 - p.FailRate) * 3.0

	var effort float64
	switch {
	case p.AvgDuration > 30:
		effort = 1.0
	case p.AvgDuration < 10:
		effort = -0.5
	}

	var triagePen float64
	if p.TriageFlag {
		triagePen = -10.0
	}

	failPen := float64(p.ConsecutiveFails) * -0.5

	return output + reliability + effort + triagePen + failPen
}

// scoreVerdict maps a productivity score to a hire/fire/promote decision.
func scoreVerdict(score float64, triageFlag bool) string {
	if triageFlag {
		return "fire"
	}
	switch {
	case score >= 6.0:
		return "promote"
	case score >= 3.0:
		return "retain"
	case score >= 1.0:
		return "monitor"
	default:
		return "fire"
	}
}

// Leaderboard ranks all agents with run history by productivity score (highest first).
// Agents with no recorded runs are omitted.
func (ps *ProfileStore) Leaderboard(ctx context.Context) ([]LeaderboardEntry, error) {
	profiles, err := ps.AllProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("leaderboard: %w", err)
	}

	entries := make([]LeaderboardEntry, 0, len(profiles))
	for _, p := range profiles {
		if len(p.RecentResults) == 0 {
			continue
		}
		s := round2(Score(p))
		entries = append(entries, LeaderboardEntry{
			Agent:       p.Name,
			Score:       s,
			Verdict:     scoreVerdict(s, p.TriageFlag),
			AvgCommits:  round2(p.AvgCommits),
			FailRate:    round2(p.FailRate),
			AvgDuration: round2(p.AvgDuration),
			ConsecFails: p.ConsecutiveFails,
			TriageFlag:  p.TriageFlag,
			RunCount:    len(p.RecentResults),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Score > entries[j].Score
	})

	for i := range entries {
		entries[i].Rank = i + 1
	}

	return entries, nil
}

// FormatLeaderboard renders the leaderboard as a human-readable table.
func FormatLeaderboard(entries []LeaderboardEntry) string {
	if len(entries) == 0 {
		return "No agent run data available yet."
	}

	var b strings.Builder
	b.WriteString("Agent Leaderboard — ranked by productivity score\n")
	b.WriteString(strings.Repeat("─", 74) + "\n")
	b.WriteString(fmt.Sprintf("%-4s %-28s %6s %-8s %7s %6s %5s\n",
		"Rank", "Agent", "Score", "Verdict", "Commits", "Fails%", "Runs"))
	b.WriteString(strings.Repeat("─", 74) + "\n")
	for _, e := range entries {
		flag := ""
		if e.TriageFlag {
			flag = " ⚑"
		}
		b.WriteString(fmt.Sprintf("%-4d %-28s %6.2f %-8s %6.0f%% %5.0f%% %5d%s\n",
			e.Rank,
			truncate(e.Agent, 28),
			e.Score,
			e.Verdict,
			e.AvgCommits*100,
			e.FailRate*100,
			e.RunCount,
			flag,
		))
	}
	return b.String()
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
