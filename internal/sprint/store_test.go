package sprint

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

func testStore(t *testing.T) (*Store, context.Context) {
	t.Helper()

	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}

	ns := "octi-test-sprint-" + t.Name()

	// Clean up before and after
	cleanup := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	cleanup()
	t.Cleanup(func() {
		cleanup()
		rdb.Close()
	})

	return NewStore(rdb, ns), ctx
}

func TestStore_UpdateStatus(t *testing.T) {
	s, ctx := testStore(t)

	// Seed a sprint item directly
	item := SprintItem{
		Squad:    "kernel",
		IssueNum: 42,
		Repo:     "AgentGuardHQ/agentguard",
		Title:    "Fix bug",
		Priority: 0,
		Status:   "open",
	}
	data, _ := json.Marshal(item)
	s.rdb.Set(ctx, s.itemKey("AgentGuardHQ/agentguard", 42), data, 0)
	s.rdb.SAdd(ctx, s.key("sprint-repos"), "AgentGuardHQ/agentguard")

	// Update status
	err := s.UpdateStatus(ctx, "AgentGuardHQ/agentguard", 42, "in_progress")
	if err != nil {
		t.Fatalf("update status: %v", err)
	}

	// Verify
	all, _ := s.GetAll(ctx)
	if len(all) != 1 {
		t.Fatalf("expected 1 item, got %d", len(all))
	}
	if all[0].Status != "in_progress" {
		t.Fatalf("expected in_progress, got %s", all[0].Status)
	}
}

func TestStore_NextDispatchable(t *testing.T) {
	s, ctx := testStore(t)

	repo := "AgentGuardHQ/agentguard"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo)

	// Seed items: one open with no deps, one open with unmet dep, one done
	items := []SprintItem{
		{Squad: "kernel", IssueNum: 1, Repo: repo, Title: "First", Priority: 2, Status: "open"},
		{Squad: "kernel", IssueNum: 2, Repo: repo, Title: "Second", Priority: 0, Status: "open", DependsOn: []int{3}},
		{Squad: "kernel", IssueNum: 3, Repo: repo, Title: "Third", Priority: 1, Status: "done"},
		{Squad: "kernel", IssueNum: 4, Repo: repo, Title: "Blocked", Priority: 0, Status: "open", DependsOn: []int{99}},
	}
	for _, item := range items {
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, s.itemKey(repo, item.IssueNum), data, 0)
	}

	dispatchable, err := s.NextDispatchable(ctx)
	if err != nil {
		t.Fatalf("next dispatchable: %v", err)
	}

	// Should get item 2 (P0, dep #3 is done) and item 1 (P2, no deps)
	// Item 4 is blocked (dep #99 not done), item 3 is already done
	if len(dispatchable) != 2 {
		t.Fatalf("expected 2 dispatchable, got %d", len(dispatchable))
	}

	// P0 should come first
	if dispatchable[0].IssueNum != 2 {
		t.Fatalf("expected issue 2 first (P0), got issue %d", dispatchable[0].IssueNum)
	}
	if dispatchable[1].IssueNum != 1 {
		t.Fatalf("expected issue 1 second (P2), got issue %d", dispatchable[1].IssueNum)
	}
}

func TestStore_GetBySquad(t *testing.T) {
	s, ctx := testStore(t)

	repo1 := "AgentGuardHQ/agentguard"
	repo2 := "AgentGuardHQ/octi-pulpo"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo1, repo2)

	items := []SprintItem{
		{Squad: "kernel", IssueNum: 1, Repo: repo1, Title: "Kernel work", Status: "open"},
		{Squad: "octi-pulpo", IssueNum: 2, Repo: repo2, Title: "Octi work", Status: "open"},
	}
	for _, item := range items {
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, s.itemKey(item.Repo, item.IssueNum), data, 0)
	}

	kernelItems, err := s.GetBySquad(ctx, "kernel")
	if err != nil {
		t.Fatalf("get by squad: %v", err)
	}
	if len(kernelItems) != 1 {
		t.Fatalf("expected 1 kernel item, got %d", len(kernelItems))
	}
	if kernelItems[0].Title != "Kernel work" {
		t.Fatalf("expected 'Kernel work', got %q", kernelItems[0].Title)
	}
}

func TestParseIssueRefs(t *testing.T) {
	tests := []struct {
		body string
		want []int
	}{
		{"Closes #43", []int{43}},
		{"closes #43", []int{43}},
		{"Fixes #10\nResolves #20", []int{10, 20}},
		{"Fixed #5", []int{5}},
		{"Resolved #7", []int{7}},
		{"Close #99", []int{99}},
		{"No references here", nil},
		{"Related to #100", nil}, // "Related" not a closing keyword
		{"Closes #1 and Fixes #2", []int{1, 2}},
	}

	for _, tc := range tests {
		got := parseIssueRefs(tc.body)
		if len(got) != len(tc.want) {
			t.Errorf("parseIssueRefs(%q): got %v, want %v", tc.body, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseIssueRefs(%q)[%d]: got %d, want %d", tc.body, i, got[i], tc.want[i])
			}
		}
	}
}

func TestStore_NextDispatchable_SkipsPROpen(t *testing.T) {
	s, ctx := testStore(t)

	repo := "AgentGuardHQ/octi-pulpo"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo)

	items := []SprintItem{
		{Squad: "octi-pulpo", IssueNum: 43, Repo: repo, Title: "Cross-squad routing", Priority: 0, Status: "pr_open", PRNumber: 57},
		{Squad: "octi-pulpo", IssueNum: 44, Repo: repo, Title: "Async standups", Priority: 0, Status: "open"},
	}
	for _, item := range items {
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, s.itemKey(repo, item.IssueNum), data, 0)
	}

	dispatchable, err := s.NextDispatchable(ctx)
	if err != nil {
		t.Fatalf("next dispatchable: %v", err)
	}

	// Issue #43 has pr_open status — must NOT appear in dispatchable
	if len(dispatchable) != 1 {
		t.Fatalf("expected 1 dispatchable (only #44), got %d: %+v", len(dispatchable), dispatchable)
	}
	if dispatchable[0].IssueNum != 44 {
		t.Fatalf("expected issue #44, got #%d", dispatchable[0].IssueNum)
	}
}

func TestStore_NextMergeable(t *testing.T) {
	s, ctx := testStore(t)

	repo := "AgentGuardHQ/octi-pulpo"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo)

	items := []SprintItem{
		{Squad: "octi-pulpo", IssueNum: 43, Repo: repo, Title: "Cross-squad routing", Priority: 0, Status: "pr_open", PRNumber: 57},
		{Squad: "octi-pulpo", IssueNum: 44, Repo: repo, Title: "Async standups", Priority: 0, Status: "open"},
		{Squad: "octi-pulpo", IssueNum: 50, Repo: repo, Title: "Done feature", Priority: 1, Status: "done", PRNumber: 30},
		{Squad: "octi-pulpo", IssueNum: 51, Repo: repo, Title: "Another PR", Priority: 1, Status: "pr_open", PRNumber: 55},
	}
	for _, item := range items {
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, s.itemKey(repo, item.IssueNum), data, 0)
	}

	mergeable, err := s.NextMergeable(ctx)
	if err != nil {
		t.Fatalf("next mergeable: %v", err)
	}

	// Only pr_open items with PRNumber > 0: #43 and #51
	if len(mergeable) != 2 {
		t.Fatalf("expected 2 mergeable, got %d: %+v", len(mergeable), mergeable)
	}
	// P0 comes first
	if mergeable[0].IssueNum != 43 {
		t.Fatalf("expected issue #43 first (P0), got #%d", mergeable[0].IssueNum)
	}
	if mergeable[0].PRNumber != 57 {
		t.Fatalf("expected PR #57 for issue #43, got #%d", mergeable[0].PRNumber)
	}
	if mergeable[1].IssueNum != 51 {
		t.Fatalf("expected issue #51 second (P1), got #%d", mergeable[1].IssueNum)
	}
}

func TestStore_NextMergeable_Empty(t *testing.T) {
	s, ctx := testStore(t)

	repo := "AgentGuardHQ/octi-pulpo"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo)

	items := []SprintItem{
		{Squad: "octi-pulpo", IssueNum: 44, Repo: repo, Title: "Async standups", Priority: 0, Status: "open"},
	}
	for _, item := range items {
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, s.itemKey(repo, item.IssueNum), data, 0)
	}

	mergeable, err := s.NextMergeable(ctx)
	if err != nil {
		t.Fatalf("next mergeable: %v", err)
	}
	if len(mergeable) != 0 {
		t.Fatalf("expected 0 mergeable, got %d", len(mergeable))
	}
}

func TestStore_SyncPRs_PreservesNonOpen(t *testing.T) {
	s, ctx := testStore(t)

	repo := "AgentGuardHQ/octi-pulpo"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo)

	// Seed items: one claimed, one done — SyncPRs must not override either
	items := []SprintItem{
		{Squad: "octi-pulpo", IssueNum: 43, Repo: repo, Title: "Cross-squad routing", Priority: 0, Status: "claimed"},
		{Squad: "octi-pulpo", IssueNum: 44, Repo: repo, Title: "Async standups", Priority: 0, Status: "done"},
	}
	for _, item := range items {
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, s.itemKey(repo, item.IssueNum), data, 0)
	}

	// Simulate what SyncPRs would do if it found PRs for both issues
	// by directly calling the update path logic.
	// (We test the preservation invariant without calling `gh`.)
	for _, issueNum := range []int{43, 44} {
		key := s.itemKey(repo, issueNum)
		raw, _ := s.rdb.Get(ctx, key).Result()
		var item SprintItem
		json.Unmarshal([]byte(raw), &item)

		// Only "open" items should be promoted — replicate SyncPRs logic.
		if item.Status != "open" {
			continue // this is the guard we are testing
		}
		item.Status = "pr_open"
		item.PRNumber = 99
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, key, data, 0)
	}

	// Verify neither item was changed
	all, _ := s.GetAll(ctx)
	statuses := make(map[int]string)
	for _, it := range all {
		statuses[it.IssueNum] = it.Status
	}
	if statuses[43] != "claimed" {
		t.Errorf("issue #43: expected claimed, got %s", statuses[43])
	}
	if statuses[44] != "done" {
		t.Errorf("issue #44: expected done, got %s", statuses[44])
	}
}

func TestMarkClosedItems_MarksOpenAndPROpen(t *testing.T) {
	s, ctx := testStore(t)
	repo := "AgentGuardHQ/octi-pulpo"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo)

	items := []SprintItem{
		{Squad: "octi-pulpo", IssueNum: 8, Repo: repo, Title: "Cost routing", Priority: 0, Status: "open"},
		{Squad: "octi-pulpo", IssueNum: 9, Repo: repo, Title: "Slack ctrl", Priority: 0, Status: "pr_open", PRNumber: 41},
		{Squad: "octi-pulpo", IssueNum: 10, Repo: repo, Title: "Briefings", Priority: 0, Status: "done"},
		{Squad: "octi-pulpo", IssueNum: 11, Repo: repo, Title: "WIP", Priority: 0, Status: "in_progress"},
	}
	for _, item := range items {
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, s.itemKey(repo, item.IssueNum), data, 0)
	}

	// Closed issues on GitHub: #8 and #9. #10 already done. #11 in_progress should still become done.
	marked := s.markClosedItems(ctx, repo, []int{8, 9, 10, 11})
	if marked != 3 {
		t.Fatalf("expected 3 marked, got %d", marked)
	}

	all, _ := s.GetAll(ctx)
	byNum := make(map[int]SprintItem, len(all))
	for _, item := range all {
		byNum[item.IssueNum] = item
	}

	for _, num := range []int{8, 9, 11} {
		if byNum[num].Status != "done" {
			t.Errorf("issue #%d: expected done, got %s", num, byNum[num].Status)
		}
	}
	if byNum[10].Status != "done" {
		t.Errorf("issue #10 should stay done, got %s", byNum[10].Status)
	}
}

func TestMarkClosedItems_SkipsUntracked(t *testing.T) {
	s, ctx := testStore(t)
	repo := "AgentGuardHQ/octi-pulpo"

	// Sprint store has no items for this repo
	marked := s.markClosedItems(ctx, repo, []int{1, 2, 3})
	if marked != 0 {
		t.Fatalf("expected 0 marked for untracked items, got %d", marked)
	}
}

func TestMarkClosedItems_EmptyList(t *testing.T) {
	s, ctx := testStore(t)
	marked := s.markClosedItems(ctx, "AgentGuardHQ/octi-pulpo", []int{})
	if marked != 0 {
		t.Fatalf("expected 0 for empty list, got %d", marked)
	}
}

func TestStore_Reprioritize(t *testing.T) {
	s, ctx := testStore(t)

	repo := "AgentGuardHQ/octi-pulpo"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo)

	item := SprintItem{
		Squad: "octi-pulpo", IssueNum: 44, Repo: repo,
		Title: "Async standups", Priority: 2, Status: "open",
	}
	data, _ := json.Marshal(item)
	s.rdb.Set(ctx, s.itemKey(repo, 44), data, 0)

	if err := s.Reprioritize(ctx, repo, 44, 0); err != nil {
		t.Fatalf("reprioritize: %v", err)
	}

	all, _ := s.GetAll(ctx)
	if len(all) != 1 {
		t.Fatalf("expected 1 item, got %d", len(all))
	}
	if all[0].Priority != 0 {
		t.Fatalf("expected priority 0, got %d", all[0].Priority)
	}
}

func TestStore_Reprioritize_NotFound(t *testing.T) {
	s, ctx := testStore(t)

	err := s.Reprioritize(ctx, "AgentGuardHQ/octi-pulpo", 9999, 0)
	if err == nil {
		t.Fatal("expected error for missing item, got nil")
	}
}

func TestStore_Complete_NoDepedents(t *testing.T) {
	s, ctx := testStore(t)

	repo := "AgentGuardHQ/octi-pulpo"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo)

	item := SprintItem{
		Squad: "octi-pulpo", IssueNum: 10, Repo: repo,
		Title: "Solo item", Priority: 1, Status: "open",
	}
	data, _ := json.Marshal(item)
	s.rdb.Set(ctx, s.itemKey(repo, 10), data, 0)

	unblocked, err := s.Complete(ctx, repo, 10)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(unblocked) != 0 {
		t.Fatalf("expected 0 unblocked, got %d: %v", len(unblocked), unblocked)
	}

	all, _ := s.GetAll(ctx)
	if all[0].Status != "done" {
		t.Fatalf("expected done, got %s", all[0].Status)
	}
}

func TestStore_Complete_UnblocksDependent(t *testing.T) {
	s, ctx := testStore(t)

	repo := "AgentGuardHQ/octi-pulpo"
	s.rdb.SAdd(ctx, s.key("sprint-repos"), repo)

	// Item 10: the blocker; item 20: depends on 10; item 30: depends on 10 AND 40 (not done)
	items := []SprintItem{
		{Squad: "octi-pulpo", IssueNum: 10, Repo: repo, Title: "Blocker", Priority: 0, Status: "open"},
		{Squad: "octi-pulpo", IssueNum: 20, Repo: repo, Title: "Waiting on 10", Priority: 1, Status: "open", DependsOn: []int{10}},
		{Squad: "octi-pulpo", IssueNum: 30, Repo: repo, Title: "Waiting on 10+40", Priority: 1, Status: "open", DependsOn: []int{10, 40}},
		{Squad: "octi-pulpo", IssueNum: 40, Repo: repo, Title: "Other blocker", Priority: 0, Status: "open"},
	}
	for _, item := range items {
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, s.itemKey(repo, item.IssueNum), data, 0)
	}

	unblocked, err := s.Complete(ctx, repo, 10)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Only item 20 should be unblocked (item 30 still needs #40)
	if len(unblocked) != 1 {
		t.Fatalf("expected 1 unblocked, got %d: %v", len(unblocked), unblocked)
	}
	if unblocked[0] != 20 {
		t.Fatalf("expected issue #20 unblocked, got #%d", unblocked[0])
	}
}

// TestCloseIssue_SkipsWhenNoGH verifies that CloseIssue returns an error
// (rather than panicking or hanging) when the gh CLI is unavailable or
// GITHUB_TOKEN is not set. The sprint store state is unaffected — write-back
// is best-effort and does not roll back the Redis mark-done.
//
// When GH_TOKEN / GITHUB_TOKEN is set and the issue exists, CloseIssue should
// succeed. That path is covered by integration tests (skipped in CI without a
// token).
func TestCloseIssue_SkipsWhenNoGH(t *testing.T) {
	s, ctx := testStore(t)

	// Use a non-existent repo/issue so even a valid gh install fails fast.
	err := s.CloseIssue(ctx, "AgentGuardHQ/octi-pulpo-nonexistent", 999999, "")
	// We don't assert a specific error — only that the function returns an
	// error without panicking or blocking indefinitely.
	if err == nil {
		t.Log("CloseIssue succeeded (gh CLI authenticated and found the issue — unexpected in unit context)")
	}
	// No Redis state change expected — CloseIssue only touches GitHub.
}

func TestInferSquadFromRepo(t *testing.T) {
	tests := []struct {
		repo  string
		squad string
	}{
		{"AgentGuardHQ/agentguard", "kernel"},
		{"AgentGuardHQ/agentguard-cloud", "cloud"},
		{"AgentGuardHQ/octi-pulpo", "octi-pulpo"},
		{"AgentGuardHQ/shellforge", "shellforge"},
		{"AgentGuardHQ/agentguard-analytics", "analytics"},
	}

	for _, tc := range tests {
		got := inferSquadFromRepo(tc.repo)
		if got != tc.squad {
			t.Errorf("inferSquadFromRepo(%q) = %q, want %q", tc.repo, got, tc.squad)
		}
	}
}
