package bootcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/redis/go-redis/v9"
)

func redisSetup(t *testing.T) (*redis.Client, string, context.Context) {
	t.Helper()
	url := os.Getenv("OCTI_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379"
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Skipf("redis url parse: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	ns := "octi-bootcheck-test-" + t.Name()
	keys, _ := rdb.Keys(ctx, ns+":*").Result()
	if len(keys) > 0 {
		rdb.Del(ctx, keys...)
	}
	t.Cleanup(func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})
	return rdb, ns, ctx
}

func TestCheckDispatchLog_Wired(t *testing.T) {
	rdb, ns, ctx := redisSetup(t)
	res := checkDispatchLog(ctx, Deps{RDB: rdb, Namespace: ns}, time.Now)
	if res.Status != StatusGreen {
		t.Fatalf("expected green, got %s: %s", res.Status, res.Message)
	}
}

func TestCheckDispatchLog_NilClient(t *testing.T) {
	res := checkDispatchLog(context.Background(), Deps{}, time.Now)
	if res.Status != StatusYellow {
		t.Fatalf("expected yellow for missing rdb, got %s", res.Status)
	}
}

func TestCheckBenchmarkCounters_Idle(t *testing.T) {
	rdb, ns, ctx := redisSetup(t)
	bt := dispatch.NewBenchmarkTracker(rdb, ns)
	res := checkBenchmarkCounters(ctx, Deps{RDB: rdb, Namespace: ns, Benchmark: bt})
	if res.Status != StatusGreen {
		t.Fatalf("idle should pass green, got %s: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "idle") {
		t.Fatalf("expected 'idle' note, got %q", res.Message)
	}
}

func TestCheckBenchmarkCounters_DispatchOnlyDerivesMetrics(t *testing.T) {
	// Regression for workspace#408 continuation: when dispatch-log has entries
	// but worker-results is empty (the common GH-Actions / Anthropic case),
	// Compute() must derive ActiveAgents / QAIX from dispatch-log so
	// bootcheck goes GREEN. Previously this returned RED ("counters unwired").
	rdb, ns, ctx := redisSetup(t)
	rec, _ := json.Marshal(map[string]any{"agent": "a", "result": "dispatched"})
	rdb.LPush(ctx, ns+":dispatch-log", rec)
	bt := dispatch.NewBenchmarkTracker(rdb, ns)
	res := checkBenchmarkCounters(ctx, Deps{RDB: rdb, Namespace: ns, Benchmark: bt})
	if res.Status != StatusGreen {
		t.Fatalf("expected green (dispatch-log fallback should wire counters), got %s: %s", res.Status, res.Message)
	}
}

func TestCheckBenchmarkCounters_NilTracker(t *testing.T) {
	res := checkBenchmarkCounters(context.Background(), Deps{})
	if res.Status != StatusYellow {
		t.Fatalf("nil tracker should yellow-skip, got %s", res.Status)
	}
}

func TestCheckHealthFresh_AllFresh(t *testing.T) {
	dir := t.TempDir()
	hf := routing.HealthFile{
		State:       "CLOSED",
		LastSuccess: time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
		Updated:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := routing.WriteDriverHealthFile(dir, "claude-code", hf); err != nil {
		t.Fatal(err)
	}
	r := routing.NewRouter(dir)
	res := checkHealthFresh(context.Background(), Deps{Router: r}, time.Now)
	if res.Status != StatusGreen {
		t.Fatalf("expected green, got %s: %s", res.Status, res.Message)
	}
}

func TestCheckHealthFresh_StaleClosedIsLie(t *testing.T) {
	dir := t.TempDir()
	stale := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	hf := routing.HealthFile{
		State:       "CLOSED",
		LastSuccess: stale,
		Updated:     stale,
	}
	if err := routing.WriteDriverHealthFile(dir, "codex", hf); err != nil {
		t.Fatal(err)
	}
	r := routing.NewRouter(dir)
	now := func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) }
	res := checkHealthFresh(context.Background(), Deps{Router: r}, now)
	if res.Status != StatusRed {
		t.Fatalf("expected red for stale CLOSED driver, got %s: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "codex") {
		t.Fatalf("expected codex in message, got %q", res.Message)
	}
}

func TestCheckHealthFresh_EmptyNameStemIsSkipped(t *testing.T) {
	// Regression: a file literally named ".json" (empty stem) used to surface
	// as an empty-name ghost entry like " (never-succeeded)" in the bootcheck
	// output. It must be skipped at the discovery layer.
	dir := t.TempDir()
	// Write a corrupt stemless health file and a valid one alongside it.
	if err := os.WriteFile(dir+"/.json", []byte(`{"state":"CLOSED"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hf := routing.HealthFile{
		State:       "CLOSED",
		LastSuccess: time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
		Updated:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := routing.WriteDriverHealthFile(dir, "claude-code", hf); err != nil {
		t.Fatal(err)
	}
	r := routing.NewRouter(dir)
	res := checkHealthFresh(context.Background(), Deps{Router: r}, time.Now)
	if res.Status != StatusGreen {
		t.Fatalf("expected green (ghost skipped), got %s: %s", res.Status, res.Message)
	}
	if strings.Contains(res.Message, " (never-succeeded)") {
		t.Fatalf("output leaked empty-name ghost entry: %q", res.Message)
	}
}

func TestCheckHealthFresh_NoDrivers(t *testing.T) {
	r := routing.NewRouter(t.TempDir())
	res := checkHealthFresh(context.Background(), Deps{Router: r}, time.Now)
	if res.Status != StatusYellow {
		t.Fatalf("no drivers should yellow, got %s", res.Status)
	}
}

func TestCheckLeaderboardSink_ActivityButEmpty(t *testing.T) {
	rdb, ns, ctx := redisSetup(t)
	rdb.LPush(ctx, ns+":dispatch-log", `{"agent":"ghost","result":"dispatched"}`)
	ps := dispatch.NewProfileStore(rdb, ns, func(string) time.Duration { return 0 })
	res := checkLeaderboardSink(ctx, Deps{RDB: rdb, Namespace: ns, Profiles: ps})
	if res.Status != StatusRed {
		t.Fatalf("expected red for activity-with-empty-leaderboard, got %s: %s", res.Status, res.Message)
	}
}

func TestCheckLeaderboardSink_Idle(t *testing.T) {
	rdb, ns, ctx := redisSetup(t)
	ps := dispatch.NewProfileStore(rdb, ns, func(string) time.Duration { return 0 })
	res := checkLeaderboardSink(ctx, Deps{RDB: rdb, Namespace: ns, Profiles: ps})
	if res.Status != StatusGreen {
		t.Fatalf("expected green (vacuous) for idle, got %s: %s", res.Status, res.Message)
	}
}

func TestCheckAdapterReachability_OK(t *testing.T) {
	fake := func(ctx context.Context, url, token string) (int, error) { return 200, nil }
	res := checkAdapterReachability(context.Background(), Deps{GitHubToken: "tok", HTTPGet: fake})
	if res.Status != StatusGreen {
		t.Fatalf("expected green, got %s: %s", res.Status, res.Message)
	}
}

func TestCheckAdapterReachability_NoToken(t *testing.T) {
	res := checkAdapterReachability(context.Background(), Deps{})
	if res.Status != StatusYellow {
		t.Fatalf("no token should yellow-skip, got %s", res.Status)
	}
}

func TestCheckAdapterReachability_ErrorIsYellow(t *testing.T) {
	fake := func(ctx context.Context, url, token string) (int, error) { return 500, nil }
	res := checkAdapterReachability(context.Background(), Deps{GitHubToken: "tok", HTTPGet: fake})
	if res.Status != StatusYellow {
		t.Fatalf("expected yellow for 500, got %s", res.Status)
	}
}

func TestRun_AllYellowWhenUnwired(t *testing.T) {
	rep := Run(context.Background(), Deps{})
	if rep.RedCount != 0 {
		t.Fatalf("unwired should produce no RED, got %d", rep.RedCount)
	}
	if len(rep.Results) != 5 {
		t.Fatalf("expected 5 checks, got %d", len(rep.Results))
	}
}

func TestRender_FormatsTable(t *testing.T) {
	rep := &CheckReport{
		StartedAt: time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
		Results: []Result{
			{Name: "dispatch_log_writable", Status: StatusGreen, Message: "ok"},
			{Name: "leaderboard_sink_wired", Status: StatusRed, Message: "unwired"},
		},
		GreenCount: 1, RedCount: 1,
	}
	var buf bytes.Buffer
	rep.Render(&buf)
	out := buf.String()
	for _, want := range []string{"dispatch_log_writable", "RED", "unwired", "WARNING"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in render output:\n%s", want, out)
		}
	}
}

func TestCache_SetGet(t *testing.T) {
	c := NewCache()
	if c.Get() != nil {
		t.Fatal("fresh cache should be nil")
	}
	r := &CheckReport{GreenCount: 3}
	c.Set(r)
	if got := c.Get(); got == nil || got.GreenCount != 3 {
		t.Fatalf("get after set: %+v", got)
	}
}
