package routing

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestPruneOrphanHealth(t *testing.T) {
	dir := t.TempDir()
	// Known driver (in tiers map)
	writeJSON(t, dir, "clawta.json", `{"state":"CLOSED"}`)
	// Orphans (pruned in Ladder Forge II)
	writeJSON(t, dir, "claude-code.json", `{"state":"OPEN"}`)
	writeJSON(t, dir, "copilot.json", `{"state":"OPEN"}`)
	writeJSON(t, dir, "goose.json", `{"state":"OPEN"}`)

	tiers := map[string]CostTier{"clawta": TierLocal}
	r := NewRouterWithTiers(dir, tiers)

	// Dry run: report but don't delete
	orphans, err := r.PruneOrphanHealth(false)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	sort.Strings(orphans)
	want := []string{"claude-code", "copilot", "goose"}
	if len(orphans) != 3 || orphans[0] != want[0] || orphans[1] != want[1] || orphans[2] != want[2] {
		t.Fatalf("dry-run orphans = %v, want %v", orphans, want)
	}
	// Files should still exist
	if _, err := os.Stat(filepath.Join(dir, "copilot.json")); err != nil {
		t.Fatalf("dry-run should not delete: %v", err)
	}

	// Real run: delete
	orphans, err = r.PruneOrphanHealth(true)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(orphans) != 3 {
		t.Fatalf("delete orphans = %v, want 3", orphans)
	}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(dir, name+".json")); !os.IsNotExist(err) {
			t.Fatalf("expected %s deleted, stat err = %v", name, err)
		}
	}
	// Known driver survived
	if _, err := os.Stat(filepath.Join(dir, "clawta.json")); err != nil {
		t.Fatalf("known driver deleted: %v", err)
	}

	// Idempotent
	orphans, err = r.PruneOrphanHealth(true)
	if err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("second run orphans = %v, want empty", orphans)
	}
}

func writeJSON(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
