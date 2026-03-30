package standup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testSetup(t *testing.T) (*Store, context.Context) {
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
	ns := "standup-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	t.Cleanup(func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
		rdb.Close()
	})
	return NewStore(rdb, ns), ctx
}

func TestReport_and_Read(t *testing.T) {
	store, ctx := testSetup(t)

	err := store.Report(ctx, "kernel", "kernel-jr",
		[]string{"Merged PR #1391"},
		[]string{"Working on #1410"},
		[]string{"#1376 needs NODE_PATH fix"},
		[]string{"analytics report"},
	)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	date := time.Now().UTC().Format("2006-01-02")
	entry, err := store.Read(ctx, "kernel", date)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Squad != "kernel" {
		t.Errorf("squad = %q, want %q", entry.Squad, "kernel")
	}
	if entry.PostedBy != "kernel-jr" {
		t.Errorf("posted_by = %q, want %q", entry.PostedBy, "kernel-jr")
	}
	if len(entry.Done) != 1 || entry.Done[0] != "Merged PR #1391" {
		t.Errorf("done = %v, want [Merged PR #1391]", entry.Done)
	}
	if len(entry.Blocked) != 1 {
		t.Errorf("blocked = %v, want 1 item", entry.Blocked)
	}
}

func TestRead_missing(t *testing.T) {
	store, ctx := testSetup(t)
	entry, err := store.Read(ctx, "nonexistent", "2000-01-01")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil for missing entry, got %+v", entry)
	}
}

func TestReadToday_multipleSquads(t *testing.T) {
	store, ctx := testSetup(t)

	squads := []string{"kernel", "octi-pulpo", "shellforge"}
	for _, sq := range squads {
		err := store.Report(ctx, sq, sq+"-em",
			[]string{"done item"},
			[]string{"doing item"},
			nil,
			nil,
		)
		if err != nil {
			t.Fatalf("Report %s: %v", sq, err)
		}
	}

	entries, err := store.ReadToday(ctx)
	if err != nil {
		t.Fatalf("ReadToday: %v", err)
	}
	if len(entries) != len(squads) {
		t.Errorf("ReadToday returned %d entries, want %d", len(entries), len(squads))
	}
}

func TestReport_overwrite(t *testing.T) {
	store, ctx := testSetup(t)

	// File initial standup
	if err := store.Report(ctx, "kernel", "agent-v1", []string{"old work"}, nil, nil, nil); err != nil {
		t.Fatalf("first Report: %v", err)
	}
	// Overwrite with updated standup
	if err := store.Report(ctx, "kernel", "agent-v2", []string{"updated work"}, nil, nil, nil); err != nil {
		t.Fatalf("second Report: %v", err)
	}

	date := time.Now().UTC().Format("2006-01-02")
	entry, err := store.Read(ctx, "kernel", date)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if entry.PostedBy != "agent-v2" {
		t.Errorf("expected overwrite: posted_by = %q, want agent-v2", entry.PostedBy)
	}
	if len(entry.Done) != 1 || entry.Done[0] != "updated work" {
		t.Errorf("expected overwrite: done = %v", entry.Done)
	}
}

func TestFormatSlack_empty(t *testing.T) {
	got := FormatSlack("2026-03-30", nil)
	if !strings.Contains(got, "No standups filed") {
		t.Errorf("expected 'No standups filed', got: %s", got)
	}
}

func TestFormatSlack_withEntries(t *testing.T) {
	entries := []Entry{
		{Squad: "kernel", Done: []string{"Merged PR #1"}, Doing: []string{"#1410"}, Blocked: []string{"#1376"}, Requests: []string{"analytics"}},
		{Squad: "shellforge", Done: []string{"Fixed bug"}, Doing: []string{"Tests"}},
	}
	got := FormatSlack("2026-03-30", entries)
	if !strings.Contains(got, "Daily Standup — 2026-03-30") {
		t.Errorf("missing date header in: %s", got)
	}
	if !strings.Contains(got, "🟡") {
		t.Errorf("expected blocked icon for kernel in: %s", got)
	}
	if !strings.Contains(got, "🟢") {
		t.Errorf("expected green icon for shellforge in: %s", got)
	}
	if !strings.Contains(got, "#1376") {
		t.Errorf("blocker not in output: %s", got)
	}
}
