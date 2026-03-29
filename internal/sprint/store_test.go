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
