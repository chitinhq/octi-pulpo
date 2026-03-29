package memory

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// testStore creates a memory Store for integration tests.
// Skips if Redis is not available. Each call uses a unique namespace derived
// from t.Name() to prevent cross-test contamination.
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
	probe := redis.NewClient(opts)
	if err := probe.Ping(context.Background()).Err(); err != nil {
		probe.Close()
		t.Skipf("skipping: redis not available: %v", err)
	}
	probe.Close()

	// Sanitize test name for use as a Redis key prefix.
	ns := "octi-test-mem-" + strings.ReplaceAll(t.Name(), "/", "-")

	ctx := context.Background()

	store, err := New(redisURL, ns)
	if err != nil {
		t.Skipf("skipping: cannot connect to redis: %v", err)
	}

	clearKeys := func() {
		opts2, _ := redis.ParseURL(redisURL)
		c := redis.NewClient(opts2)
		keys, _ := c.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			c.Del(ctx, keys...)
		}
		c.Close()
	}
	clearKeys() // remove any leftover keys from a previous run
	t.Cleanup(func() {
		clearKeys()
		store.Close()
	})

	return store
}

func TestBackwardCompat_NoSquad(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	_, err := store.Put(ctx, "agent-a", "root memory entry", []string{"test"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	results, err := store.Recall(ctx, "root memory", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result in root namespace")
	}
	if results[0].Content != "root memory entry" {
		t.Fatalf("unexpected content: %q", results[0].Content)
	}
}

func TestSquadIsolation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	squadA := store.WithSquad("squad-a")
	_, err := squadA.Put(ctx, "agent-a", "squad-a secret learning", []string{"secret"})
	if err != nil {
		t.Fatalf("Put squad-a: %v", err)
	}

	// Squad B should not see squad A's memory.
	squadB := store.WithSquad("squad-b")
	results, err := squadB.Recall(ctx, "squad-a secret", 5)
	if err != nil {
		t.Fatalf("Recall squad-b: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for squad-b, got %d", len(results))
	}
}

func TestSquadIsolation_RootDoesNotSeeSquad(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	squad := store.WithSquad("isolated-squad")
	_, err := squad.Put(ctx, "agent-x", "isolated squad memory", []string{"isolated"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Root recall should not surface squad-scoped entries.
	results, err := store.Recall(ctx, "isolated squad memory", 5)
	if err != nil {
		t.Fatalf("Recall root: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("root namespace should not see squad memory, got %d results", len(results))
	}
}

func TestSquadIsolation_SameSquadCanRecall(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	squad := store.WithSquad("recall-squad")
	_, err := squad.Put(ctx, "agent-y", "squad internal knowledge", []string{"knowledge"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	results, err := squad.Recall(ctx, "squad internal", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected squad to recall its own memories")
	}
	if results[0].Content != "squad internal knowledge" {
		t.Fatalf("unexpected content: %q", results[0].Content)
	}
}

func TestRegisterSquad(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.RegisterSquad(ctx, "frontend"); err != nil {
		t.Fatalf("RegisterSquad: %v", err)
	}
	if err := store.RegisterSquad(ctx, "infra"); err != nil {
		t.Fatalf("RegisterSquad: %v", err)
	}

	names, err := store.SquadNames(ctx)
	if err != nil {
		t.Fatalf("SquadNames: %v", err)
	}

	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["frontend"] {
		t.Error("expected 'frontend' in squad names")
	}
	if !nameSet["infra"] {
		t.Error("expected 'infra' in squad names")
	}
}

func TestRecallCrossSquad(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// Store in root namespace.
	_, err := store.Put(ctx, "root-agent", "root level insight", []string{"root"})
	if err != nil {
		t.Fatalf("Put root: %v", err)
	}

	// Store in squad-a (and register it).
	if err := store.RegisterSquad(ctx, "squad-a"); err != nil {
		t.Fatalf("RegisterSquad: %v", err)
	}
	_, err = store.WithSquad("squad-a").Put(ctx, "agent-a", "squad-a insight", []string{"squad-a"})
	if err != nil {
		t.Fatalf("Put squad-a: %v", err)
	}

	// Cross-squad should find both.
	results, err := store.RecallCrossSquad(ctx, "insight", 10)
	if err != nil {
		t.Fatalf("RecallCrossSquad: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 cross-squad results, got %d", len(results))
	}

	contents := make(map[string]bool)
	for _, r := range results {
		contents[r.Content] = true
	}
	if !contents["root level insight"] {
		t.Error("cross-squad should include root namespace memory")
	}
	if !contents["squad-a insight"] {
		t.Error("cross-squad should include squad-a memory")
	}
}

func TestRecallCrossSquad_Deduplication(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	_, err := store.Put(ctx, "agent", "unique content xyz", []string{"dedup"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Cross-squad with no registered squads — should only return root entries once.
	results, err := store.RecallCrossSquad(ctx, "unique content xyz", 10)
	if err != nil {
		t.Fatalf("RecallCrossSquad: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 result (no duplicates), got %d", len(results))
	}
}

func TestWithSquad_EmptySquadNS(t *testing.T) {
	store := testStore(t)
	// WithSquad("") should return the same store unchanged.
	scoped := store.WithSquad("")
	if scoped != store {
		t.Error("WithSquad(\"\") should return the same store")
	}
}
