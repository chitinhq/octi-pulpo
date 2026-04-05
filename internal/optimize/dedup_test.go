package optimize

import (
	"testing"
)

func TestTaskHash_Deterministic(t *testing.T) {
	h1 := TaskHash("code-gen", "fix the bug", "chitinhq/octi-pulpo")
	h2 := TaskHash("code-gen", "fix the bug", "chitinhq/octi-pulpo")
	if h1 != h2 {
		t.Errorf("same inputs should produce same hash: %s vs %s", h1, h2)
	}
}

func TestTaskHash_DifferentInputs(t *testing.T) {
	h1 := TaskHash("code-gen", "fix the bug", "chitinhq/octi-pulpo")
	h2 := TaskHash("code-gen", "fix the bug", "chitinhq/shellforge")
	if h1 == h2 {
		t.Error("different repos should produce different hashes")
	}
}

func TestTaskHash_DifferentTypes(t *testing.T) {
	h1 := TaskHash("code-gen", "fix the bug", "repo")
	h2 := TaskHash("pr-review", "fix the bug", "repo")
	if h1 == h2 {
		t.Error("different task types should produce different hashes")
	}
}

func TestTaskHash_Length(t *testing.T) {
	h := TaskHash("triage", "classify this", "repo")
	if len(h) != 16 {
		t.Errorf("expected 16-char hash prefix, got %d chars: %s", len(h), h)
	}
}

func TestDefaultTTLs(t *testing.T) {
	cases := map[string]bool{
		"triage":    true,
		"pr-review": true,
		"qa":        true,
		"code-gen":  true,
		"bugfix":    true,
	}
	for k := range cases {
		if _, ok := DefaultTTLs[k]; !ok {
			t.Errorf("missing TTL for task type %q", k)
		}
	}
}

func TestNewDedup(t *testing.T) {
	d := NewDedup(nil, "test")
	if d.namespace != "test" {
		t.Errorf("expected namespace test, got %s", d.namespace)
	}
	if len(d.ttls) != len(DefaultTTLs) {
		t.Errorf("expected %d TTLs, got %d", len(DefaultTTLs), len(d.ttls))
	}
}
