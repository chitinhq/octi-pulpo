package standup_test

import (
	"context"
	"testing"

	"github.com/chitinhq/octi-pulpo/internal/standup"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) *standup.Store {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	t.Cleanup(func() { rdb.Close() })

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}

	ns := "test-standup-" + t.Name()
	// Flush test keys on cleanup
	t.Cleanup(func() {
		keys, _ := rdb.Keys(ctx, ns+":standup:*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	})

	return standup.New(rdb, ns)
}

func TestStandupStore_ReportAndRead(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	entry := standup.Entry{
		Squad:    "octi-pulpo",
		Done:     []string{"Merged PR #42", "Closed 2 stale issues"},
		Doing:    []string{"Implementing async standups (#44)"},
		Blocked:  []string{},
		Requests: []string{"Need analytics report"},
	}

	if err := s.Report(ctx, entry); err != nil {
		t.Fatalf("Report: %v", err)
	}

	got, err := s.Read(ctx, "octi-pulpo")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil {
		t.Fatal("expected entry, got nil")
	}
	if got.Squad != "octi-pulpo" {
		t.Errorf("Squad = %q, want %q", got.Squad, "octi-pulpo")
	}
	if len(got.Done) != 2 {
		t.Errorf("len(Done) = %d, want 2", len(got.Done))
	}
	if len(got.Doing) != 1 {
		t.Errorf("len(Doing) = %d, want 1", len(got.Doing))
	}
	if got.Date == "" {
		t.Error("Date should be set by Report")
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp should be set by Report")
	}
}

func TestStandupStore_ReportOverwrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first := standup.Entry{Squad: "kernel", Doing: []string{"item1"}}
	second := standup.Entry{Squad: "kernel", Doing: []string{"item2", "item3"}}

	if err := s.Report(ctx, first); err != nil {
		t.Fatalf("first Report: %v", err)
	}
	if err := s.Report(ctx, second); err != nil {
		t.Fatalf("second Report: %v", err)
	}

	got, err := s.Read(ctx, "kernel")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Doing) != 2 {
		t.Errorf("len(Doing) = %d, want 2 (second write should overwrite first)", len(got.Doing))
	}
}

func TestStandupStore_ReadMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.Read(ctx, "nonexistent-squad")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing entry, got %+v", got)
	}
}

func TestStandupStore_NilSlicesSerializeAsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	entry := standup.Entry{Squad: "shellforge"} // all slices nil

	if err := s.Report(ctx, entry); err != nil {
		t.Fatalf("Report: %v", err)
	}

	got, err := s.Read(ctx, "shellforge")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Done == nil {
		t.Error("Done should be [] not nil after round-trip")
	}
	if got.Doing == nil {
		t.Error("Doing should be [] not nil after round-trip")
	}
	if got.Blocked == nil {
		t.Error("Blocked should be [] not nil after round-trip")
	}
	if got.Requests == nil {
		t.Error("Requests should be [] not nil after round-trip")
	}
}

func TestStandupStore_Daily(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	squads := []string{"alpha", "beta", "gamma"}
	for _, sq := range squads {
		if err := s.Report(ctx, standup.Entry{Squad: sq, Done: []string{"thing"}}); err != nil {
			t.Fatalf("Report %s: %v", sq, err)
		}
	}

	entries, err := s.Daily(ctx)
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("Daily returned %d entries, want 3", len(entries))
	}

	// Verify all squads are present
	seen := make(map[string]bool)
	for _, e := range entries {
		seen[e.Squad] = true
	}
	for _, sq := range squads {
		if !seen[sq] {
			t.Errorf("squad %q missing from Daily result", sq)
		}
	}
}

func TestStandupStore_DailyEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	entries, err := s.Daily(ctx)
	if err != nil {
		t.Fatalf("Daily on empty store: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}
