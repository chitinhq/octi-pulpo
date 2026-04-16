package dispatch

import (
	"testing"
)

func TestInferSquad(t *testing.T) {
	// Dead squad names (cloud, octi-pulpo, studio, office-sim) were
	// removed from the knownSquads list in octi#271 Phase 1. They now
	// fall through to the first-segment fallback.
	tests := []struct {
		agentID string
		want    string
	}{
		{"kernel-sr", "kernel"},
		{"kernel-qa", "kernel"},
		{"kernel-em", "kernel"}, // prefix "kernel-" still matches even though em is a fossil role
		{"shellforge-sr", "shellforge"},
		{"clawta-sr", "clawta"},
		{"sentinel-sr", "sentinel"},
		{"llmint-sr", "llmint"},
		{"octi-sr", "octi"},
		{"workspace-sr", "workspace"},
		{"ganglia-sr", "ganglia"},

		// Suffix matching for agents like "ci-triage-agent-kernel"
		{"ci-triage-agent-kernel", "kernel"},
		{"triage-failing-ci-agent", "triage"}, // no known repo match, falls through to first segment

		// Dead-squad names fall through to first-segment fallback now.
		{"cloud-sr", "cloud"},
		{"studio-sr", "studio"},
		{"office-sim-sr", "office"}, // "office" is first segment; "office-sim" no longer recognised

		// Fallback to first segment
		{"unknown-agent", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			got := inferSquad(tt.agentID)
			if got != tt.want {
				t.Errorf("inferSquad(%q) = %q, want %q", tt.agentID, got, tt.want)
			}
		})
	}
}

func TestNewSignalWatcher_Defaults(t *testing.T) {
	d, _ := testSetup(t)
	rdb := d.RedisClient()

	sw := NewSignalWatcher(d, rdb, "octi-test")

	if sw.dispatcher != d {
		t.Fatal("dispatcher mismatch")
	}
	if sw.namespace != "octi-test" {
		t.Fatalf("namespace mismatch: got %s", sw.namespace)
	}
	if len(sw.repoSeniors) == 0 {
		t.Fatal("expected repo seniors to be populated")
	}
	// Every LiveRepos entry must be mapped; otherwise a squad-era
	// senior silently drops need-help signals for a real repo.
	for _, repo := range LiveRepos {
		if sw.repoSeniors[repo] == "" {
			t.Errorf("expected senior mapping for live repo %q", repo)
		}
	}
	if sw.repoSeniors["kernel"] != "kernel-sr" {
		t.Fatalf("expected kernel senior to be kernel-sr, got %s", sw.repoSeniors["kernel"])
	}
}
