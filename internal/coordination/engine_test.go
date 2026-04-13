package coordination

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testSetup creates a coordination Engine backed by real Redis.
// Requires Redis on localhost:6379 (the standard dev setup).
// Tests are skipped gracefully if Redis is not available.
func testSetup(t *testing.T) (*Engine, context.Context) {
	t.Helper()

	redisURL := "redis://localhost:6379"
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}

	// Unique namespace per test to avoid cross-contamination.
	ns := "coord-test-" + strings.ReplaceAll(t.Name(), "/", "-")

	cleanup := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	}
	t.Cleanup(cleanup)

	return &Engine{rdb: rdb, ns: ns}, ctx
}

func TestClaimTask_StoresAndReturnsValidClaim(t *testing.T) {
	e, ctx := testSetup(t)

	claim, err := e.ClaimTask(ctx, "test-agent", "build octi-pulpo", 60)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claim.AgentID != "test-agent" {
		t.Errorf("AgentID: got %q, want %q", claim.AgentID, "test-agent")
	}
	if claim.Task != "build octi-pulpo" {
		t.Errorf("Task: got %q, want %q", claim.Task, "build octi-pulpo")
	}
	if claim.TTLSeconds != 60 {
		t.Errorf("TTLSeconds: got %d, want 60", claim.TTLSeconds)
	}
	if claim.ClaimID == "" {
		t.Error("ClaimID should not be empty")
	}
	if claim.ClaimedAt == "" {
		t.Error("ClaimedAt should not be empty")
	}
}

func TestClaimTask_AppearsInActiveClaims(t *testing.T) {
	e, ctx := testSetup(t)

	if _, err := e.ClaimTask(ctx, "agent-a", "run tests", 60); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	claims, err := e.ActiveClaims(ctx)
	if err != nil {
		t.Fatalf("ActiveClaims: %v", err)
	}
	for _, c := range claims {
		if c.AgentID == "agent-a" && c.Task == "run tests" {
			return // found
		}
	}
	t.Error("claim for agent-a not found in ActiveClaims")
}

func TestActiveClaims_EmptyWhenNoClaims(t *testing.T) {
	e, ctx := testSetup(t)

	claims, err := e.ActiveClaims(ctx)
	if err != nil {
		t.Fatalf("ActiveClaims: %v", err)
	}
	if len(claims) != 0 {
		t.Errorf("expected 0 claims, got %d", len(claims))
	}
}

func TestReleaseClaim_RemovesFromActiveClaims(t *testing.T) {
	e, ctx := testSetup(t)

	if _, err := e.ClaimTask(ctx, "agent-b", "deploy", 120); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	if err := e.ReleaseClaim(ctx, "agent-b"); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}

	// TTL key is gone; ActiveClaims filters by TTL existence.
	claims, err := e.ActiveClaims(ctx)
	if err != nil {
		t.Fatalf("ActiveClaims: %v", err)
	}
	for _, c := range claims {
		if c.AgentID == "agent-b" {
			t.Error("released claim should not appear in ActiveClaims")
		}
	}
}

func TestBroadcast_SignalAppearsInRecentSignals(t *testing.T) {
	e, ctx := testSetup(t)

	if err := e.Broadcast(ctx, "agent-c", "completed", "test run done"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}

	signals, err := e.RecentSignals(ctx, 10)
	if err != nil {
		t.Fatalf("RecentSignals: %v", err)
	}
	for _, s := range signals {
		if s.AgentID == "agent-c" && s.Type == "completed" && s.Payload == "test run done" {
			return // found
		}
	}
	t.Error("broadcast signal not found in RecentSignals")
}

func TestRecentSignals_EmptyWhenNoSignals(t *testing.T) {
	e, ctx := testSetup(t)

	signals, err := e.RecentSignals(ctx, 10)
	if err != nil {
		t.Fatalf("RecentSignals: %v", err)
	}
	if len(signals) != 0 {
		t.Errorf("expected 0 signals, got %d", len(signals))
	}
}

func TestBroadcast_SetsTimestamp(t *testing.T) {
	e, ctx := testSetup(t)
	before := time.Now().UTC().Add(-time.Second)

	if err := e.Broadcast(ctx, "agent-d", "blocked", "waiting on review"); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}

	signals, err := e.RecentSignals(ctx, 5)
	if err != nil {
		t.Fatalf("RecentSignals: %v", err)
	}
	for _, s := range signals {
		if s.AgentID != "agent-d" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, s.Timestamp)
		if err != nil {
			t.Fatalf("invalid timestamp %q: %v", s.Timestamp, err)
		}
		if ts.Before(before) {
			t.Errorf("timestamp %v is before test start %v", ts, before)
		}
		return
	}
	t.Error("signal not found")
}

func TestClaimTask_ClaimIDContainsAgentID(t *testing.T) {
	e, ctx := testSetup(t)

	claim, err := e.ClaimTask(ctx, "my-agent", "some-task", 30)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if !strings.HasPrefix(claim.ClaimID, "my-agent:") {
		t.Errorf("ClaimID %q should start with agent ID prefix", claim.ClaimID)
	}
}

// TestActiveClaims_PrunesOrphanedZsetMembers covers issue #206:
// when a claim holder dies without calling ReleaseClaim, the `claim:<agent>`
// SET key auto-expires via Redis TTL but the zset member lingers forever.
// ActiveClaims must ZREM stale members on read.
func TestActiveClaims_PrunesOrphanedZsetMembers(t *testing.T) {
	e, ctx := testSetup(t)
	zkey := e.key("active-claims")

	// Inject an orphaned claim: score far in the past, TTL short, no `claim:` SET key.
	orphan := Claim{
		ClaimID:    "ghost:1",
		AgentID:    "ghost-agent",
		Task:       "wedged forever",
		ClaimedAt:  time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
		TTLSeconds: 60,
	}
	data, _ := json.Marshal(orphan)
	pastMilli := time.Now().Add(-1 * time.Hour).UnixMilli()
	if err := e.rdb.ZAdd(ctx, zkey, redis.Z{Score: float64(pastMilli), Member: data}).Err(); err != nil {
		t.Fatalf("seed zset: %v", err)
	}

	// Pre-check: member is present.
	if n, _ := e.rdb.ZCard(ctx, zkey).Result(); n != 1 {
		t.Fatalf("pre: want 1 zset member, got %d", n)
	}

	// Add a live claim so dispatch has something to return.
	if _, err := e.ClaimTask(ctx, "live-agent", "real work", 300); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	claims, err := e.ActiveClaims(ctx)
	if err != nil {
		t.Fatalf("ActiveClaims: %v", err)
	}

	// Orphan must not appear.
	for _, c := range claims {
		if c.AgentID == "ghost-agent" {
			t.Error("orphaned ghost-agent claim leaked into ActiveClaims")
		}
	}

	// Orphan must be ZREMed — the zset should only contain the live claim.
	n, err := e.rdb.ZCard(ctx, zkey).Result()
	if err != nil {
		t.Fatalf("ZCard: %v", err)
	}
	if n != 1 {
		t.Errorf("expected zset pruned to 1 member, got %d", n)
	}

	// Confirm the surviving member is the live one.
	members, _ := e.rdb.ZRange(ctx, zkey, 0, -1).Result()
	if len(members) != 1 || !strings.Contains(members[0], "live-agent") {
		t.Errorf("surviving member not live-agent: %v", members)
	}
}

// TestActiveClaims_LegacyMemberMissingTTLUsesDefault covers the fallback path
// in ActiveClaims where a pre-#206 zset member was persisted without a
// `ttl_seconds` field. The prune arithmetic must treat ttl<=0 as the default
// (900s), so a legacy member whose score is ~1h old (well under 900s... wait:
// 900s == 15min, so 1h > 900s) MUST be pruned — and a recent legacy member
// (score = now) MUST survive even without an explicit ttl_seconds, provided
// its `claim:<agent>` SET key exists.
func TestActiveClaims_LegacyMemberMissingTTLUsesDefault(t *testing.T) {
	e, ctx := testSetup(t)
	zkey := e.key("active-claims")

	// Legacy payload: no ttl_seconds field. Marshal a minimal JSON by hand so
	// the Claim.TTLSeconds defaults to 0 on the wire (tests the fallback code
	// path even if struct tags change in the future).
	recentLegacy := `{"claim_id":"legacy:recent","agent_id":"legacy-recent","task":"x","claimed_at":"2026-04-13T00:00:00Z"}`
	oldLegacy := `{"claim_id":"legacy:old","agent_id":"legacy-old","task":"x","claimed_at":"2026-04-12T00:00:00Z"}`

	now := time.Now().UnixMilli()
	// Recent: score = now, default TTL 900s would keep it alive.
	if err := e.rdb.ZAdd(ctx, zkey, redis.Z{Score: float64(now), Member: recentLegacy}).Err(); err != nil {
		t.Fatalf("seed recent: %v", err)
	}
	// Old: score = 1h ago, default TTL 900s means expired by ~45min.
	if err := e.rdb.ZAdd(ctx, zkey, redis.Z{Score: float64(now - 3600*1000), Member: oldLegacy}).Err(); err != nil {
		t.Fatalf("seed old: %v", err)
	}

	// The recent legacy entry needs a live `claim:` SET key to survive the
	// exists==0 branch (pruning requires BOTH a live SET key AND unexpired
	// score+ttl to keep the entry).
	if err := e.rdb.Set(ctx, e.key("claim:legacy-recent"), recentLegacy, 900*time.Second).Err(); err != nil {
		t.Fatalf("seed SET: %v", err)
	}

	claims, err := e.ActiveClaims(ctx)
	if err != nil {
		t.Fatalf("ActiveClaims: %v", err)
	}

	var sawRecent, sawOld bool
	for _, c := range claims {
		if c.AgentID == "legacy-recent" {
			sawRecent = true
		}
		if c.AgentID == "legacy-old" {
			sawOld = true
		}
	}
	if !sawRecent {
		t.Error("recent legacy claim (missing ttl_seconds) should survive via default 900s fallback")
	}
	if sawOld {
		t.Error("1h-old legacy claim should be pruned — default TTL 900s is well expired")
	}

	// Expired legacy must be ZREMed from the zset, not just filtered.
	members, _ := e.rdb.ZRange(ctx, zkey, 0, -1).Result()
	for _, m := range members {
		if strings.Contains(m, "legacy-old") {
			t.Error("expired legacy member should be ZREMed from zset")
		}
	}
}

func TestClose_NoError(t *testing.T) {
	e, _ := testSetup(t)
	// Close is called via t.Cleanup, but we verify explicit close works too.
	if err := e.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
