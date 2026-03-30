package standup

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// testStore creates a standup Store for integration tests.
// Skips if Redis is not available. Each call uses a unique namespace to
// prevent cross-test contamination, and registers a cleanup to delete
// all keys created during the test.
func testStore(t *testing.T) *Store {
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
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		rdb.Close()
		t.Skipf("skipping: redis not available: %v", err)
	}

	ns := "octi-test-standup-" + strings.ReplaceAll(t.Name(), "/", "-")
	t.Cleanup(func() {
		ctx := context.Background()
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})

	return New(rdb, ns)
}

func TestPost_and_Read(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	r := Report{
		Squad:    "kernel",
		Done:     []string{"Merged #1391"},
		Doing:    []string{"Working on #1410"},
		Blocked:  []string{},
		Requests: []string{"Need analytics report"},
		PostedBy: "test-agent",
	}

	if err := s.Post(ctx, r); err != nil {
		t.Fatalf("Post: %v", err)
	}

	got, err := s.Read(ctx, "kernel", "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil {
		t.Fatal("expected a report, got nil")
	}
	if got.Squad != "kernel" {
		t.Errorf("Squad = %q, want %q", got.Squad, "kernel")
	}
	if len(got.Done) != 1 || got.Done[0] != "Merged #1391" {
		t.Errorf("Done = %v, want [Merged #1391]", got.Done)
	}
	if got.PostedBy != "test-agent" {
		t.Errorf("PostedBy = %q, want %q", got.PostedBy, "test-agent")
	}
}

func TestRead_missing_returns_nil(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	got, err := s.Read(ctx, "nonexistent-squad", "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing squad, got %+v", got)
	}
}

func TestPost_overwrites_same_day(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first := Report{Squad: "octi-pulpo", Done: []string{"v1"}, Doing: []string{"a"}, PostedBy: "agent-1"}
	if err := s.Post(ctx, first); err != nil {
		t.Fatalf("Post first: %v", err)
	}

	second := Report{Squad: "octi-pulpo", Done: []string{"v2"}, Doing: []string{"b"}, PostedBy: "agent-2"}
	if err := s.Post(ctx, second); err != nil {
		t.Fatalf("Post second: %v", err)
	}

	got, err := s.Read(ctx, "octi-pulpo", "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil {
		t.Fatal("expected a report, got nil")
	}
	if got.Done[0] != "v2" {
		t.Errorf("expected amended report (v2), got %q", got.Done[0])
	}
}

func TestReadAll(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	squads := []string{"kernel", "octi-pulpo", "shellforge"}
	for _, sq := range squads {
		r := Report{Squad: sq, Done: []string{"did something"}, Doing: []string{"doing something"}, PostedBy: "test"}
		if err := s.Post(ctx, r); err != nil {
			t.Fatalf("Post %s: %v", sq, err)
		}
	}

	all, err := s.ReadAll(ctx, "")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ReadAll returned %d reports, want 3", len(all))
	}

	// Results should be sorted by squad name
	names := make([]string, len(all))
	for i, r := range all {
		names[i] = r.Squad
	}
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("ReadAll not sorted: %v", names)
		}
	}
}

func TestReadAll_empty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	all, err := s.ReadAll(ctx, "")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty slice for fresh namespace, got %d items", len(all))
	}
}
