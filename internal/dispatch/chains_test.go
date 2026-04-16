package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultChains_KeyAgentsExist(t *testing.T) {
	chains := DefaultChains()

	// Verify key chain entries exist. Squad-era fan-outs
	// (jared-conductor, director, *-em) and dead-repo chains
	// (cloud-sr/qa, octi-pulpo-sr, studio-sr, office-sim-sr) were
	// excised in octi#271 Phase 1; absence is enforced by
	// TestNoFossilAgentsInChains.
	required := []string{
		"kernel-sr", "shellforge-sr",
		"kernel-qa", "shellforge-qa",
		"workspace-pr-review-agent",
	}
	for _, name := range required {
		if _, ok := chains[name]; !ok {
			t.Errorf("expected chain entry for %s", name)
		}
	}
}

func TestCompletionAction_Targets_Success(t *testing.T) {
	action := CompletionAction{
		OnSuccess: []string{"qa-agent"},
		OnFailure: []string{"triage-agent"},
		OnCommit:  []string{"reviewer"},
	}

	targets := action.Targets(0, false)
	if len(targets) != 1 || targets[0] != "qa-agent" {
		t.Fatalf("expected [qa-agent], got %v", targets)
	}
}

func TestCompletionAction_Targets_Failure(t *testing.T) {
	action := CompletionAction{
		OnSuccess: []string{"qa-agent"},
		OnFailure: []string{"triage-agent"},
		OnCommit:  []string{"reviewer"},
	}

	targets := action.Targets(1, false)
	if len(targets) != 1 || targets[0] != "triage-agent" {
		t.Fatalf("expected [triage-agent], got %v", targets)
	}
}

func TestCompletionAction_Targets_SuccessWithCommits(t *testing.T) {
	action := CompletionAction{
		OnSuccess: []string{"qa-agent"},
		OnCommit:  []string{"reviewer"},
	}

	targets := action.Targets(0, true)
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %v", targets)
	}
	if targets[0] != "qa-agent" || targets[1] != "reviewer" {
		t.Fatalf("expected [qa-agent, reviewer], got %v", targets)
	}
}

func TestCompletionAction_Targets_Dedup(t *testing.T) {
	action := CompletionAction{
		OnSuccess: []string{"same-agent"},
		OnCommit:  []string{"same-agent"},
	}

	targets := action.Targets(0, true)
	if len(targets) != 1 {
		t.Fatalf("expected deduplication to 1 target, got %v", targets)
	}
}

func TestCompletionAction_Targets_Empty(t *testing.T) {
	action := CompletionAction{}

	targets := action.Targets(0, false)
	if len(targets) != 0 {
		t.Fatalf("expected empty targets, got %v", targets)
	}
}

func TestTriggerChains_DispatchesTargets(t *testing.T) {
	d, ctx := testSetup(t)

	chains := ChainConfig{
		"test-sr": {
			OnCommit: []string{"test-qa"},
		},
	}

	results := TriggerChains(ctx, d, chains, "test-sr", 0, true)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Agent != "test-qa" {
		t.Fatalf("expected test-qa, got %s", results[0].Agent)
	}
	if results[0].Action != "dispatched" {
		t.Fatalf("expected dispatched, got %s (reason: %s)", results[0].Action, results[0].Reason)
	}
}

func TestTriggerChains_NoChain(t *testing.T) {
	d, ctx := testSetup(t)

	chains := DefaultChains()
	results := TriggerChains(ctx, d, chains, "nonexistent-agent", 0, false)
	if len(results) != 0 {
		t.Fatalf("expected no results for unknown agent, got %d", len(results))
	}
}

func TestTriggerChains_FailurePath(t *testing.T) {
	d, ctx := testSetup(t)

	chains := ChainConfig{
		"failing-sr": {
			OnSuccess: []string{"should-not-fire"},
			OnFailure: []string{"triage-agent"},
		},
	}

	results := TriggerChains(ctx, d, chains, "failing-sr", 1, false)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Agent != "triage-agent" {
		t.Fatalf("expected triage-agent, got %s", results[0].Agent)
	}
}

func TestTriggerChains_MultipleTargets(t *testing.T) {
	d, ctx := testSetup(t)

	chains := ChainConfig{
		"conductor": {
			OnSuccess: []string{"em-a", "em-b", "em-c"},
		},
	}

	results := TriggerChains(ctx, d, chains, "conductor", 0, false)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	agents := make(map[string]bool)
	for _, r := range results {
		agents[r.Agent] = true
	}
	for _, want := range []string{"em-a", "em-b", "em-c"} {
		if !agents[want] {
			t.Errorf("expected %s in results", want)
		}
	}
}

func TestCheckForCommits_Positive(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "server", "logs")
	os.MkdirAll(logDir, 0755)

	// Write a log file with push indicator
	logContent := `Starting agent kernel-sr
Checking out branch feat/new-feature
Making changes...
Pushing branch feat/new-feature
remote: Create a pull request
Done.
`
	os.WriteFile(filepath.Join(logDir, "kernel-sr.log"), []byte(logContent), 0644)

	if !CheckForCommits("kernel-sr", dir) {
		t.Fatal("expected CheckForCommits to return true")
	}
}

func TestCheckForCommits_Negative(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "server", "logs")
	os.MkdirAll(logDir, 0755)

	// Write a log without push indicators
	logContent := `Starting agent kernel-sr
Reading files...
No changes needed.
Done.
`
	os.WriteFile(filepath.Join(logDir, "kernel-sr.log"), []byte(logContent), 0644)

	if CheckForCommits("kernel-sr", dir) {
		t.Fatal("expected CheckForCommits to return false")
	}
}

func TestCheckForCommits_NoLogFile(t *testing.T) {
	dir := t.TempDir()

	if CheckForCommits("nonexistent-agent", dir) {
		t.Fatal("expected CheckForCommits to return false when no log exists")
	}
}

// --- Integration: verify chain + dispatch pipeline ---

func TestChainIntegration_SRtoQAtoReviewer(t *testing.T) {
	d, ctx := testSetup(t)
	chains := DefaultChains()

	// Simulate kernel-sr completing with commits
	results := TriggerChains(ctx, d, chains, "kernel-sr", 0, true)
	foundQA := false
	for _, r := range results {
		if r.Agent == "kernel-qa" {
			foundQA = true
			if r.Action != "dispatched" {
				t.Fatalf("kernel-qa expected dispatched, got %s", r.Action)
			}
		}
	}
	if !foundQA {
		t.Fatal("expected kernel-qa to be dispatched from kernel-sr chain")
	}

	// Release claim so kernel-qa can be dispatched again if needed
	d.ReleaseClaim(context.Background(), "kernel-qa")

	// Simulate kernel-qa completing successfully
	results = TriggerChains(ctx, d, chains, "kernel-qa", 0, false)
	foundReviewer := false
	for _, r := range results {
		if r.Agent == "workspace-pr-review-agent" {
			foundReviewer = true
			if r.Action != "dispatched" {
				t.Fatalf("reviewer expected dispatched, got %s", r.Action)
			}
		}
	}
	if !foundReviewer {
		t.Fatal("expected workspace-pr-review-agent to be dispatched from kernel-qa chain")
	}
}
