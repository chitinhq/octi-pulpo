package dispatch

import (
	"testing"
	"time"
)

// TestPipelineIntegration_SmokeTest exercises the full assembly line logic:
// triage → queue classification → model routing → stagger → adapter selection → escalation.
// No real CLI calls or GitHub API — everything in-memory.
func TestPipelineIntegration_SmokeTest(t *testing.T) {
	// ClaudeCodeAdapter.CanAccept checks for ANTHROPIC_API_KEY (legacy guard).
	// In the swarm, claude -p uses Max plan auth, but the guard still exists.
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	// Setup all components
	mr := NewModelRouter()
	qm := NewQueueMachine()
	st := NewStaggerTracker(nil, "test")
	em := NewEscalationManager(mr)
	claudeAdapter := NewClaudeCodeAdapter("claude", "/tmp/workspace")
	copilotAdapter := NewCopilotCLIAdapter("copilot", "/tmp/workspace")

	// --- Step 1: Triage complexity inference ---
	// "test: smoke test" with tier:b-scope → complexity:low
	complexity := inferComplexity("tier:b-scope", "test: smoke test for dual-agent swarm pipeline")
	if complexity != "complexity:low" {
		t.Fatalf("inferComplexity = %q, want complexity:low", complexity)
	}
	t.Log("✓ Triage: test: prefix → complexity:low")

	// --- Step 2: Queue classification ---
	// Fresh issue with tier:b-scope, complexity:low → QueueIntake (no planned label)
	labels := []string{"tier:b-scope", "complexity:low"}
	queue := qm.ClassifyQueue(labels)
	if queue != QueueIntake {
		t.Fatalf("ClassifyQueue(%v) = %d, want QueueIntake(%d)", labels, queue, QueueIntake)
	}
	t.Log("✓ Queue: new issue → QueueIntake")

	// --- Step 3: Model routing ---
	comp := qm.ComplexityFromLabels(labels)
	if comp != "low" {
		t.Fatalf("ComplexityFromLabels = %q, want low", comp)
	}
	copilotModel := mr.CopilotModel(comp)
	claudeModel := mr.ClaudeModel(comp)
	if copilotModel != "gpt-5.4-nano" {
		t.Fatalf("CopilotModel(low) = %q, want gpt-5.4-nano", copilotModel)
	}
	if claudeModel != "sonnet" {
		t.Fatalf("ClaudeModel(low) = %q, want sonnet", claudeModel)
	}
	t.Log("✓ Model routing: low → gpt-5.4-nano (copilot), sonnet (claude)")

	// --- Step 4: Stagger picks first platform ---
	now := time.Now()
	platform := st.NextPlatform(true, true)
	if platform != "copilot" {
		t.Fatalf("NextPlatform (first) = %q, want copilot", platform)
	}
	st.RecordDispatch(platform, now)
	t.Log("✓ Stagger: first dispatch → copilot")

	// --- Step 5: Adapter accepts the task ---
	task := &Task{
		ID:      "smoke-1",
		Type:    "plan", // Q1 intake does planning
		Prompt:  "Plan implementation for smoke test issue",
		Context: comp,
	}
	if !copilotAdapter.CanAccept(task) {
		t.Fatal("CopilotCLIAdapter.CanAccept(plan) = false, want true")
	}
	if !claudeAdapter.CanAccept(task) {
		t.Fatal("ClaudeCodeAdapter.CanAccept(plan) = false, want true")
	}
	t.Log("✓ Both adapters accept plan tasks")

	// --- Step 6: Simulate Q1 success, advance to Q2 ---
	nextLabel := qm.NextLabel(QueueIntake, true)
	if nextLabel != "planned" {
		t.Fatalf("NextLabel(QueueIntake, true) = %q, want planned", nextLabel)
	}
	labels = append(labels, "planned")
	queue = qm.ClassifyQueue(labels)
	if queue != QueueBuild {
		t.Fatalf("ClassifyQueue with planned = %d, want QueueBuild(%d)", queue, QueueBuild)
	}
	t.Log("✓ Q1→Q2: planned label moves issue to QueueBuild")

	// --- Step 7: Stagger alternates to claude ---
	platform = st.NextPlatform(true, true)
	if platform != "claude" {
		t.Fatalf("NextPlatform (second) = %q, want claude", platform)
	}
	st.RecordDispatch(platform, now.Add(1*time.Minute))
	t.Log("✓ Stagger: second dispatch → claude (alternation)")

	// --- Step 8: Simulate Q2 success, advance to Q3 ---
	nextLabel = qm.NextLabel(QueueBuild, true)
	if nextLabel != "implemented" {
		t.Fatalf("NextLabel(QueueBuild, true) = %q, want implemented", nextLabel)
	}
	labels = append(labels, "implemented")
	queue = qm.ClassifyQueue(labels)
	if queue != QueueValidate {
		t.Fatalf("ClassifyQueue with implemented = %d, want QueueValidate(%d)", queue, QueueValidate)
	}
	t.Log("✓ Q2→Q3: implemented label moves issue to QueueValidate")

	// --- Step 9: Simulate Q3 success ---
	nextLabel = qm.NextLabel(QueueValidate, true)
	if nextLabel != "validated" {
		t.Fatalf("NextLabel(QueueValidate, true) = %q, want validated", nextLabel)
	}
	labels = append(labels, "validated")
	queue = qm.ClassifyQueue(labels)
	if queue != QueueDone {
		t.Fatalf("ClassifyQueue with validated = %d, want QueueDone(%d)", queue, QueueDone)
	}
	t.Log("✓ Q3→Done: validated label completes the pipeline")

	// --- Step 10: Simulate Q3 failure → needs-fix → back to Q2 ---
	failLabel := qm.NextLabel(QueueValidate, false)
	if failLabel != "needs-fix" {
		t.Fatalf("NextLabel(QueueValidate, false) = %q, want needs-fix", failLabel)
	}
	failLabels := []string{"tier:b-scope", "complexity:low", "planned", "implemented", "needs-fix"}
	queue = qm.ClassifyQueue(failLabels)
	if queue != QueueBuild {
		t.Fatalf("ClassifyQueue with needs-fix = %d, want QueueBuild(%d)", queue, QueueBuild)
	}
	t.Log("✓ Q3 failure: needs-fix re-enters QueueBuild")

	// --- Step 11: Escalation ladder ---
	// Copilot: nano → mini
	d := em.Escalate("copilot-cli", "gpt-5.4-nano", 1)
	if d.Action != "retry-same-platform" || d.Model != "gpt-5.4-mini" {
		t.Fatalf("Escalate copilot nano = %+v, want retry/mini", d)
	}
	// Copilot: 5.4 → cross-platform to claude
	d = em.Escalate("copilot-cli", "gpt-5.4", 2)
	if d.Action != "cross-platform" || d.Platform != "claude-code" {
		t.Fatalf("Escalate copilot 5.4 = %+v, want cross-platform/claude-code", d)
	}
	// Claude: sonnet → opus
	d = em.Escalate("claude-code", "sonnet", 3)
	if d.Action != "retry-same-platform" || d.Model != "opus" {
		t.Fatalf("Escalate claude sonnet = %+v, want retry/opus", d)
	}
	// Claude: opus → human
	d = em.Escalate("claude-code", "opus", 3)
	if d.Action != "human" {
		t.Fatalf("Escalate claude opus = %+v, want human", d)
	}
	// Too many attempts → human regardless
	d = em.Escalate("copilot-cli", "gpt-5.4-nano", 4)
	if d.Action != "human" {
		t.Fatalf("Escalate 4 attempts = %+v, want human", d)
	}
	t.Log("✓ Escalation: nano→mini→5.4→cross-platform→sonnet→opus→human")

	// --- Step 12: Daily cap enforcement ---
	freshSt := NewStaggerTracker(nil, "test")
	for i := 0; i < 8; i++ {
		freshSt.RecordDispatch("copilot", now.Add(time.Duration(i)*time.Second))
	}
	if freshSt.IsUnderDailyCap("copilot", now) {
		t.Fatal("IsUnderDailyCap(copilot) = true after 8 dispatches, want false")
	}
	if !freshSt.IsUnderDailyCap("claude", now) {
		t.Fatal("IsUnderDailyCap(claude) = false with 0 dispatches, want true")
	}
	t.Log("✓ Daily cap: copilot capped at 8, claude uncapped")

	// --- Step 13: Priority queue ordering ---
	counts := map[Queue]int{
		QueueValidate: 1,
		QueueBuild:    3,
		QueueIntake:   5,
	}
	top := qm.PickHighestPriority(counts)
	if top != QueueValidate {
		t.Fatalf("PickHighestPriority = %d, want QueueValidate(%d)", top, QueueValidate)
	}
	t.Log("✓ Priority: QueueValidate > QueueBuild > QueueIntake")

	// --- Step 14: Groom trigger ---
	if !qm.NeedsGroom(3) {
		t.Fatal("NeedsGroom(3) = false, want true (< 5 threshold)")
	}
	if qm.NeedsGroom(5) {
		t.Fatal("NeedsGroom(5) = true, want false (>= 5 threshold)")
	}
	t.Log("✓ Groom trigger: fires when intake < 5")

	t.Log("\n=== Full pipeline smoke test PASSED ===")
}
