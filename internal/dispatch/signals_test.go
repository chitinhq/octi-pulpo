package dispatch

import (
	"testing"
)

func TestInferSquad(t *testing.T) {
	tests := []struct {
		agentID string
		want    string
	}{
		{"kernel-sr", "kernel"},
		{"kernel-qa", "kernel"},
		{"kernel-em", "kernel"},
		{"cloud-sr", "cloud"},
		{"cloud-qa", "cloud"},
		{"shellforge-sr", "shellforge"},
		{"octi-pulpo-sr", "octi-pulpo"},
		{"studio-sr", "studio"},
		{"office-sim-sr", "office-sim"},

		// Suffix matching for agents like "ci-triage-agent-cloud"
		{"ci-triage-agent-cloud", "cloud"},
		{"triage-failing-ci-agent", "triage"}, // no known squad match, falls through to first segment

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
	if len(sw.squadSeniors) == 0 {
		t.Fatal("expected squad seniors to be populated")
	}
	if len(sw.allEMs) == 0 {
		t.Fatal("expected allEMs to be populated")
	}
	if sw.squadSeniors["kernel"] != "kernel-sr" {
		t.Fatalf("expected kernel senior to be kernel-sr, got %s", sw.squadSeniors["kernel"])
	}
}
