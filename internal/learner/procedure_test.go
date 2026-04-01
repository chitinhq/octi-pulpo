package learner

import "testing"

func TestParseEpisode(t *testing.T) {
	content := `Task: fix the null pointer in auth handler
Type: bugfix | Repo: AgentGuardHQ/octi-pulpo | Priority: high
Outcome: completed | Adapter: anthropic-cascade:claude-haiku-4-5-20251001
Tokens: 2500 in / 800 out | Cost: $0.0100
Summary: Fixed by adding nil check in middleware/auth.go line 47`

	ep := parseEpisode(content)

	if ep.taskType != "bugfix" {
		t.Errorf("expected bugfix, got %s", ep.taskType)
	}
	if ep.repo != "AgentGuardHQ/octi-pulpo" {
		t.Errorf("expected AgentGuardHQ/octi-pulpo, got %s", ep.repo)
	}
	if ep.status != "completed" {
		t.Errorf("expected completed, got %s", ep.status)
	}
	if ep.prompt != "fix the null pointer in auth handler" {
		t.Errorf("expected prompt, got %s", ep.prompt)
	}
}

func TestParseEpisode_Failed(t *testing.T) {
	content := `Task: deploy new version
Type: code-gen | Repo: AgentGuardHQ/shellforge | Priority: normal
Outcome: failed | Adapter: anthropic
Error: shellforge exited: exit status 1`

	ep := parseEpisode(content)
	if ep.status != "failed" {
		t.Errorf("expected failed, got %s", ep.status)
	}
	if ep.taskType != "code-gen" {
		t.Errorf("expected code-gen, got %s", ep.taskType)
	}
}

func TestBuildProcedure(t *testing.T) {
	episodes := []episodeData{
		{taskType: "bugfix", repo: "AgentGuardHQ/octi-pulpo", status: "completed", prompt: "fix auth bug"},
		{taskType: "bugfix", repo: "AgentGuardHQ/octi-pulpo", status: "completed", prompt: "fix session bug"},
		{taskType: "bugfix", repo: "AgentGuardHQ/octi-pulpo", status: "failed", prompt: "fix crash"},
	}

	proc := buildProcedure("bugfix:AgentGuardHQ/octi-pulpo", episodes)

	if proc.TimesUsed != 3 {
		t.Errorf("expected 3 times used, got %d", proc.TimesUsed)
	}
	// 2 of 3 succeeded
	expectedRate := 2.0 / 3.0
	if proc.SuccessRate < expectedRate-0.01 || proc.SuccessRate > expectedRate+0.01 {
		t.Errorf("expected success rate ~%.2f, got %.2f", expectedRate, proc.SuccessRate)
	}
	if proc.Pattern != "bugfix:AgentGuardHQ/octi-pulpo" {
		t.Errorf("expected pattern, got %s", proc.Pattern)
	}
}

func TestBuildProcedure_AllSucceeded(t *testing.T) {
	episodes := []episodeData{
		{taskType: "triage", repo: "repo", status: "completed", prompt: "classify issue"},
		{taskType: "triage", repo: "repo", status: "completed", prompt: "classify bug"},
	}

	proc := buildProcedure("triage:repo", episodes)
	if proc.SuccessRate != 1.0 {
		t.Errorf("expected 100%% success, got %.2f", proc.SuccessRate)
	}
}

func TestNewProcedureExtractor(t *testing.T) {
	pe := NewProcedureExtractor(nil)
	if pe == nil {
		t.Error("expected non-nil extractor")
	}
}
