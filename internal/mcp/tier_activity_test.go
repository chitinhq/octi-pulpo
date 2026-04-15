package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/redis/go-redis/v9"
)

// newTestRedis returns a real Redis client or skips the test if Redis is
// unreachable. Uses a per-test namespace that is cleaned up via t.Cleanup.
func newTestRedis(t *testing.T) (*redis.Client, string) {
	t.Helper()
	opts, err := redis.ParseURL("redis://localhost:6379")
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}
	ns := "tier-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	t.Cleanup(func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})
	return rdb, ns
}

func pushRecord(t *testing.T, rdb *redis.Client, key string, rec dispatch.DispatchRecord) {
	t.Helper()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := rdb.LPush(context.Background(), key, data).Err(); err != nil {
		t.Fatalf("lpush: %v", err)
	}
}

func TestTierActivitySummary_GroupsByTier(t *testing.T) {
	rdb, ns := newTestRedis(t)
	key := ns + ":dispatch-log"
	now := time.Now().UTC()

	pushRecord(t, rdb, key, dispatch.DispatchRecord{Agent: "a1", Driver: "clawta", Tier: "local", Timestamp: now.Add(-30 * time.Minute).Format(time.RFC3339)})
	pushRecord(t, rdb, key, dispatch.DispatchRecord{Agent: "a2", Driver: "gh-actions", Tier: "actions", Timestamp: now.Add(-1 * time.Hour).Format(time.RFC3339)})
	pushRecord(t, rdb, key, dispatch.DispatchRecord{Agent: "a3", Driver: "gh-actions", Tier: "actions", Timestamp: now.Add(-2 * time.Hour).Format(time.RFC3339)})
	// legacy entry — no tier field
	pushRecord(t, rdb, key, dispatch.DispatchRecord{Agent: "a4", Driver: "", Timestamp: now.Add(-3 * time.Hour).Format(time.RFC3339)})

	sum, err := tierActivitySummary(context.Background(), rdb, ns, 24, 500)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if got := sum.Tiers["local"].Dispatches; got != 1 {
		t.Errorf("local=%d, want 1", got)
	}
	if got := sum.Tiers["actions"].Dispatches; got != 2 {
		t.Errorf("actions=%d, want 2", got)
	}
	if got := sum.Tiers["unknown"].Dispatches; got != 1 {
		t.Errorf("unknown=%d, want 1", got)
	}
	if got := sum.Tiers["cloud"].Dispatches; got != 0 {
		t.Errorf("cloud=%d, want 0 (T3 not online)", got)
	}
	if sum.Scanned != 4 {
		t.Errorf("scanned=%d, want 4", sum.Scanned)
	}
	if sum.Tiers["actions"].LastAt == "" {
		t.Errorf("actions last_at should be populated")
	}
}

func TestTierActivitySummary_WindowExcludesOld(t *testing.T) {
	rdb, ns := newTestRedis(t)
	key := ns + ":dispatch-log"
	now := time.Now().UTC()

	pushRecord(t, rdb, key, dispatch.DispatchRecord{Agent: "recent", Tier: "local", Timestamp: now.Add(-1 * time.Hour).Format(time.RFC3339)})
	pushRecord(t, rdb, key, dispatch.DispatchRecord{Agent: "old", Tier: "local", Timestamp: now.Add(-48 * time.Hour).Format(time.RFC3339)})

	sum, err := tierActivitySummary(context.Background(), rdb, ns, 24, 500)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if got := sum.Tiers["local"].Dispatches; got != 1 {
		t.Errorf("local=%d in 24h window, want 1 (old entry should be excluded)", got)
	}
}

func TestClassifyTier(t *testing.T) {
	cases := []struct {
		name   string
		driver string
		evt    dispatch.Event
		want   string
	}{
		{"actions", "gh-actions", dispatch.Event{}, "actions"},
		{"clawta-local", "clawta", dispatch.Event{}, "local"},
		{"openclaw-local", "openclaw", dispatch.Event{}, "local"},
		{"anthropic-cloud", "anthropic", dispatch.Event{}, "cloud"},
		{"needs-human-label", "", dispatch.Event{Type: dispatch.EventIssueLabeled, Payload: map[string]string{"label": "needs-human"}}, "human"},
		{"blank", "", dispatch.Event{}, "unknown"},
		{"mystery", "mystery-driver", dispatch.Event{}, "unknown"},
	}
	for _, c := range cases {
		if got := dispatch.ClassifyTier(c.driver, c.evt); got != c.want {
			t.Errorf("%s: ClassifyTier(%q) = %q, want %q", c.name, c.driver, got, c.want)
		}
	}
}
