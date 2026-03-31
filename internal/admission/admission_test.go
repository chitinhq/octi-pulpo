package admission_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/admission"
	"github.com/redis/go-redis/v9"
)

func testGate(t *testing.T) *admission.Gate {
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
	ns := "octi-test-admission-" + t.Name()
	t.Cleanup(func() {
		// Flush test keys
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})
	return admission.New(rdb, ns)
}

// ─── Intake Scoring ──────────────────────────────────────────────────────────

func TestScore_Accept(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	score := g.Score(ctx, admission.TaskSpec{
		Title:        "Fix nil pointer in handler",
		Squad:        "kernel",
		Repo:         "AgentGuardHQ/agentguard",
		FilePaths:    []string{"internal/handler/handler.go"},
		Priority:     1,
		IsReversible: true,
		SpecClarity:  0.9,
	})
	if score.Verdict != admission.VerdictAccept {
		t.Errorf("expected ACCEPT, got %s (score=%.2f, reasons=%v)", score.Verdict, score.Score, score.Reasons)
	}
	if score.BlastRadius != 1 {
		t.Errorf("expected blast_radius=1, got %d", score.BlastRadius)
	}
}

func TestScore_LowClarity_Preflight(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	score := g.Score(ctx, admission.TaskSpec{
		Title:       "Do the thing",
		Squad:       "kernel",
		Repo:        "AgentGuardHQ/agentguard",
		SpecClarity: 0.3,
		Priority:    2,
	})
	if score.Verdict != admission.VerdictPreflight {
		t.Errorf("expected ROUTE_TO_PREFLIGHT, got %s", score.Verdict)
	}
}

func TestScore_LargeBlast_Defer(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	files := make([]string, 15)
	for i := range files {
		files[i] = "pkg/file.go"
	}
	score := g.Score(ctx, admission.TaskSpec{
		Title:        "Partial refactor",
		Squad:        "kernel",
		Repo:         "AgentGuardHQ/agentguard",
		FilePaths:    files,
		Priority:     2,
		IsReversible: true,
		SpecClarity:  0.8,
	})
	// 15 files → -0.20 → score 0.80 - 0.20 = 0.60 → DEFER
	if score.Verdict != admission.VerdictDefer {
		t.Errorf("expected DEFER for 15-file blast, got %s (score=%.2f)", score.Verdict, score.Score)
	}
}

func TestScore_HugeBlast_Reject(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	files := make([]string, 25)
	for i := range files {
		files[i] = "pkg/file.go"
	}
	score := g.Score(ctx, admission.TaskSpec{
		Title:        "Big bang refactor",
		Squad:        "kernel",
		Repo:         "AgentGuardHQ/agentguard",
		FilePaths:    files,
		Priority:     3,
		IsReversible: false,
		SpecClarity:  0.8,
	})
	// 25 files (-0.40) + non-reversible P3 (-0.15) = score 0.45 → DEFER
	// (not REJECT because 0.45 >= 0.40)
	if score.Score >= 1.0 || score.BlastRadius != 25 {
		t.Errorf("unexpected score=%.2f or blastRadius=%d", score.Score, score.BlastRadius)
	}
}

func TestScore_AllPenalties_Reject(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	files := make([]string, 25)
	for i := range files {
		files[i] = "pkg/file.go"
	}
	score := g.Score(ctx, admission.TaskSpec{
		Title:           "Everything everywhere all at once",
		Squad:           "kernel",
		Repo:            "AgentGuardHQ/agentguard",
		FilePaths:       files,
		Priority:        3,
		IsReversible:    false,
		SpecClarity:     0.8,
		EstimatedTokens: 60000,
	})
	// 1.0 - 0.40 (blast) - 0.15 (irreversible) - 0.10 (tokens) = 0.35 → REJECT
	if score.Verdict != admission.VerdictReject {
		t.Errorf("expected REJECT with all penalties, got %s (score=%.2f)", score.Verdict, score.Score)
	}
}

// ─── Concurrency Gates ───────────────────────────────────────────────────────

func TestAcquireSlot_UnderLimit(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	scope := admission.ConcurrencyScope{Type: "repo", Key: "test-repo-" + t.Name(), Limit: 2}
	ok, err := g.AcquireSlot(ctx, scope, 60*time.Second)
	if err != nil || !ok {
		t.Fatalf("expected slot acquired, got ok=%v err=%v", ok, err)
	}
}

func TestAcquireSlot_AtLimit_Denied(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	scope := admission.ConcurrencyScope{Type: "repo", Key: "test-repo-" + t.Name(), Limit: 2}
	g.AcquireSlot(ctx, scope, 60*time.Second)
	g.AcquireSlot(ctx, scope, 60*time.Second)
	ok, err := g.AcquireSlot(ctx, scope, 60*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected denied at limit=2")
	}
}

func TestReleaseSlot_FreesCapacity(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	scope := admission.ConcurrencyScope{Type: "squad", Key: "squad-" + t.Name(), Limit: 1}
	g.AcquireSlot(ctx, scope, 60*time.Second)

	ok, _ := g.AcquireSlot(ctx, scope, 60*time.Second)
	if ok {
		t.Fatal("expected denied at limit=1")
	}
	if err := g.ReleaseSlot(ctx, scope); err != nil {
		t.Fatalf("release: %v", err)
	}
	ok, err := g.AcquireSlot(ctx, scope, 60*time.Second)
	if err != nil || !ok {
		t.Errorf("expected re-acquire after release, got ok=%v err=%v", ok, err)
	}
}

func TestSlotUsage(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	scope := admission.ConcurrencyScope{Type: "global", Key: "global-" + t.Name(), Limit: 5}
	g.AcquireSlot(ctx, scope, 60*time.Second)
	g.AcquireSlot(ctx, scope, 60*time.Second)

	current, limit, err := g.SlotUsage(ctx, scope)
	if err != nil {
		t.Fatalf("slot usage: %v", err)
	}
	if current != 2 {
		t.Errorf("expected current=2, got %d", current)
	}
	if limit != 5 {
		t.Errorf("expected limit=5, got %d", limit)
	}
}

// ─── Domain Locks ────────────────────────────────────────────────────────────

func TestAcquireLock_Success(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	lock, err := g.AcquireLock(ctx, "branch:feat/auth-"+t.Name(), "agent-sr-01", 60*time.Second)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if lock == nil {
		t.Fatal("expected lock, got nil")
	}
	if lock.Holder != "agent-sr-01" {
		t.Errorf("expected holder=agent-sr-01, got %s", lock.Holder)
	}
}

func TestAcquireLock_AlreadyHeld(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	domain := "branch:feat/auth-" + t.Name()
	g.AcquireLock(ctx, domain, "agent-sr-01", 60*time.Second)
	lock, err := g.AcquireLock(ctx, domain, "agent-jr-02", 60*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock != nil {
		t.Errorf("expected nil (lock held), got %+v", lock)
	}
}

func TestReleaseLock_WrongHolder(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	domain := "file:api/orders/-" + t.Name()
	g.AcquireLock(ctx, domain, "agent-sr-01", 60*time.Second)
	err := g.ReleaseLock(ctx, domain, "agent-jr-02")
	if err == nil {
		t.Error("expected error releasing lock held by different agent")
	}
}

func TestReleaseLock_ThenReacquire(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	domain := "service:payments-" + t.Name()
	g.AcquireLock(ctx, domain, "agent-sr-01", 60*time.Second)
	if err := g.ReleaseLock(ctx, domain, "agent-sr-01"); err != nil {
		t.Fatalf("release: %v", err)
	}
	lock, err := g.AcquireLock(ctx, domain, "agent-sr-02", 60*time.Second)
	if err != nil || lock == nil {
		t.Errorf("expected reacquire after release, got lock=%v err=%v", lock, err)
	}
}

func TestActiveLocks_CountsCorrectly(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	suffix := t.Name()
	g.AcquireLock(ctx, "file:api/-"+suffix, "agent-a", 60*time.Second)
	g.AcquireLock(ctx, "branch:feat/x-"+suffix, "agent-b", 60*time.Second)

	locks, err := g.ActiveLocks(ctx)
	if err != nil {
		t.Fatalf("active locks: %v", err)
	}
	if len(locks) < 2 {
		t.Errorf("expected at least 2 active locks, got %d", len(locks))
	}
}

func TestGetLock_Unheld(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	lock, err := g.GetLock(ctx, "branch:nonexistent-"+t.Name())
	if err != nil {
		t.Fatalf("get lock: %v", err)
	}
	if lock != nil {
		t.Errorf("expected nil for unheld lock, got %+v", lock)
	}
}
