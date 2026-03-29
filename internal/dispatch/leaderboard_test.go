package dispatch

import (
	"strings"
	"testing"
)

func TestScore(t *testing.T) {
	tests := []struct {
		name    string
		profile AgentProfile
		wantMin float64
		wantMax float64
	}{
		{
			name: "high performer — commits every run, long duration, zero failures",
			profile: AgentProfile{
				RecentResults:    []RunResult{{ExitCode: 0, Duration: 90, HadCommits: true}},
				AvgCommits:       1.0,
				FailRate:         0.0,
				AvgDuration:      90.0,
				ConsecutiveFails: 0,
				TriageFlag:       false,
			},
			wantMin: 8.9, // 5 + 3 + 1 = 9
			wantMax: 9.1,
		},
		{
			name: "idle agent — no commits, short runs, no failures",
			profile: AgentProfile{
				RecentResults:    []RunResult{{ExitCode: 0, Duration: 5}},
				AvgCommits:       0.0,
				FailRate:         0.0,
				AvgDuration:      5.0,
				ConsecutiveFails: 0,
			},
			wantMin: 2.4, // 0 + 3 - 0.5 = 2.5
			wantMax: 2.6,
		},
		{
			name: "failing agent — triage flag, 3 consecutive fails",
			profile: AgentProfile{
				RecentResults:    []RunResult{{ExitCode: 1, Duration: 5}},
				AvgCommits:       0.0,
				FailRate:         1.0,
				AvgDuration:      5.0,
				ConsecutiveFails: 3,
				TriageFlag:       true,
			},
			wantMin: -12.1, // 0 + 0 - 0.5 - 10 - 1.5 = -12
			wantMax: -11.9,
		},
		{
			name: "mixed performer — some commits, moderate fail rate",
			profile: AgentProfile{
				RecentResults:    []RunResult{{ExitCode: 0, Duration: 60, HadCommits: true}},
				AvgCommits:       0.5,
				FailRate:         0.2,
				AvgDuration:      60.0,
				ConsecutiveFails: 0,
			},
			wantMin: 5.8, // 2.5 + 2.4 + 1 = 5.9
			wantMax: 6.0,
		},
		{
			name: "no history — returns zero",
			profile: AgentProfile{
				RecentResults: nil,
			},
			wantMin: 0,
			wantMax: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Score(tt.profile)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("Score() = %v, want [%v, %v]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestScoreVerdict(t *testing.T) {
	tests := []struct {
		score      float64
		triageFlag bool
		want       string
	}{
		{9.0, false, "promote"},
		{6.0, false, "promote"},
		{5.9, false, "retain"},
		{3.0, false, "retain"},
		{2.9, false, "monitor"},
		{1.0, false, "monitor"},
		{0.9, false, "fire"},
		{-5.0, false, "fire"},
		{9.0, true, "fire"}, // triage overrides score
	}

	for _, tt := range tests {
		got := scoreVerdict(tt.score, tt.triageFlag)
		if got != tt.want {
			t.Errorf("scoreVerdict(%v, triage=%v) = %q, want %q", tt.score, tt.triageFlag, got, tt.want)
		}
	}
}

func TestFormatLeaderboard(t *testing.T) {
	t.Run("empty returns placeholder", func(t *testing.T) {
		out := FormatLeaderboard(nil)
		if !strings.Contains(out, "No agent run data") {
			t.Errorf("unexpected output for empty leaderboard: %q", out)
		}
	})

	t.Run("entries render correctly", func(t *testing.T) {
		entries := []LeaderboardEntry{
			{Rank: 1, Agent: "kernel-sr", Score: 9.0, Verdict: "promote", AvgCommits: 1.0, FailRate: 0.0, RunCount: 8},
			{Rank: 2, Agent: "cloud-sr", Score: 3.5, Verdict: "retain", AvgCommits: 0.5, FailRate: 0.1, RunCount: 5},
			{Rank: 3, Agent: "idle-sr", Score: 0.5, Verdict: "monitor", AvgCommits: 0.0, FailRate: 0.0, RunCount: 3},
			{Rank: 4, Agent: "broken-sr", Score: -2.0, Verdict: "fire", TriageFlag: true, RunCount: 2},
		}
		out := FormatLeaderboard(entries)

		for _, want := range []string{"kernel-sr", "promote", "cloud-sr", "retain", "idle-sr", "monitor", "broken-sr", "fire", "⚑"} {
			if !strings.Contains(out, want) {
				t.Errorf("FormatLeaderboard output missing %q\n%s", want, out)
			}
		}
	})
}

func TestRound2(t *testing.T) {
	if round2(3.14159) != 3.14 {
		t.Errorf("round2(3.14159) = %v, want 3.14", round2(3.14159))
	}
	if round2(0.005) != 0.01 {
		t.Errorf("round2(0.005) = %v, want 0.01", round2(0.005))
	}
}

func TestTruncate(t *testing.T) {
	if truncate("short", 10) != "short" {
		t.Error("truncate should not modify short strings")
	}
	got := truncate("very-long-agent-name-here", 10)
	if len([]rune(got)) > 10 {
		t.Errorf("truncate result too long: %q", got)
	}
}
