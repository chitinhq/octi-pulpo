package dispatch

import (
	"testing"
	"time"
)

func TestBrain_Tick_EmptyQueue(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	// Tick on empty queue should not panic or error
	brain.Tick(ctx)

	// Verify queue is still empty
	depth, err := d.PendingCount(ctx)
	if err != nil {
		t.Fatalf("pending count: %v", err)
	}
	if depth != 0 {
		t.Fatalf("expected empty queue, got %d", depth)
	}
}

func TestBrain_Stats(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	stats := brain.Stats(ctx)
	if stats["chain_count"] != len(DefaultChains()) {
		t.Fatalf("chain_count mismatch: %v", stats["chain_count"])
	}
	if stats["tick_interval"] != "1m0s" {
		t.Fatalf("tick_interval mismatch: %v", stats["tick_interval"])
	}
}

func TestBrain_SetTickInterval(t *testing.T) {
	d, _ := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	brain.SetTickInterval(30 * time.Second)
	if brain.tickInterval != 30*time.Second {
		t.Fatalf("expected 30s, got %s", brain.tickInterval)
	}
}

func TestFormatChainGraph(t *testing.T) {
	chains := ChainConfig{
		"test-sr": {
			OnCommit:  []string{"test-qa"},
			OnFailure: []string{"test-triage"},
		},
	}

	graph := FormatChainGraph(chains)
	if graph == "" {
		t.Fatal("expected non-empty graph")
	}

	// Should contain the agent name and arrows
	if !containsString(graph, "test-sr") {
		t.Fatal("expected test-sr in graph")
	}
	if !containsString(graph, "test-qa") {
		t.Fatal("expected test-qa in graph")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
