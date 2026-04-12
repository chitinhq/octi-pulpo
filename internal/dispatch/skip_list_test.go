package dispatch

import (
	"testing"
	"time"
)

func TestSkipList_InMemory(t *testing.T) {
	sl := NewSkipList(nil, "test")

	if sl.IsSkipped("octi#119") {
		t.Error("expected not skipped initially")
	}

	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")
	if sl.IsSkipped("octi#119") {
		t.Error("expected not skipped after 2 rejections")
	}

	sl.RecordRejection("octi#119")
	if !sl.IsSkipped("octi#119") {
		t.Error("expected skipped after 3 rejections")
	}

	sl.Clear("octi#119")
	if sl.IsSkipped("octi#119") {
		t.Error("expected not skipped after clear")
	}
}

func TestSkipList_Expiry(t *testing.T) {
	sl := NewSkipList(nil, "test")
	sl.TTL = 1 * time.Millisecond

	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")

	if !sl.IsSkipped("octi#119") {
		t.Fatal("expected skipped")
	}

	time.Sleep(5 * time.Millisecond)
	sl.ExpireOld()

	if sl.IsSkipped("octi#119") {
		t.Error("expected expired after TTL")
	}
}

func TestSkipList_SkipFor(t *testing.T) {
	sl := NewSkipList(nil, "test")

	// SkipFor bypasses the rejection threshold and applies a per-entry TTL.
	sl.SkipFor("octi#333", "env_blocked: uncommitted changes", 50*time.Millisecond)
	if !sl.IsSkipped("octi#333") {
		t.Fatal("expected skipped after SkipFor")
	}

	if got := sl.SkipReason("octi#333"); got != "env_blocked: uncommitted changes" {
		t.Errorf("expected reason recorded, got %q", got)
	}

	// Short TTL should cause lazy expiry on next IsSkipped call.
	time.Sleep(60 * time.Millisecond)
	if sl.IsSkipped("octi#333") {
		t.Error("expected lazy expiry after per-entry TTL")
	}
}

func TestSkipList_PerEntryTTL(t *testing.T) {
	sl := NewSkipList(nil, "test")
	sl.TTL = 1 * time.Hour // default TTL irrelevant for SkipFor

	// Two entries with different TTLs.
	sl.SkipFor("fast#1", "short", 20*time.Millisecond)
	sl.SkipFor("slow#2", "long", 10*time.Second)

	time.Sleep(30 * time.Millisecond)
	sl.ExpireOld()

	if sl.IsSkipped("fast#1") {
		t.Error("fast#1 should have expired")
	}
	if !sl.IsSkipped("slow#2") {
		t.Error("slow#2 should still be skipped")
	}
}

func TestClassifyDispatchFailure(t *testing.T) {
	tests := []struct {
		reason   string
		wantKind string
		wantTTL  time.Duration
	}{
		{"repo workspace has uncommitted changes:  M .gitignore", "env_blocked", 15 * time.Minute},
		{"repo clawta is on branch main and auto-checkout of master failed", "env_blocked", 15 * time.Minute},
		{"repo /home/jared/workspace/foo is not a git repository", "env_blocked", 15 * time.Minute},
		{"budget check failed", "budget_blocked", 1 * time.Hour},
		{"cooldown active (5h59m14s remaining)", "budget_blocked", 1 * time.Hour},
		{`Model "claude-sonnet-4-6-20250514" from --model flag is not available.`, "driver_unavailable", 30 * time.Minute},
		{"some random agent error", "", 0},
	}
	for _, tc := range tests {
		kind, ttl := classifyDispatchFailure(tc.reason)
		if kind != tc.wantKind || ttl != tc.wantTTL {
			t.Errorf("classify(%q) = (%s, %v), want (%s, %v)",
				tc.reason, kind, ttl, tc.wantKind, tc.wantTTL)
		}
	}
}

func TestSkipList_ListAll(t *testing.T) {
	sl := NewSkipList(nil, "test")
	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")
	sl.RecordRejection("octi#119")
	sl.RecordRejection("chitin#5")
	sl.RecordRejection("chitin#5")
	sl.RecordRejection("chitin#5")

	all := sl.ListAll()
	if len(all) != 2 {
		t.Errorf("expected 2 skipped, got %d", len(all))
	}
}
