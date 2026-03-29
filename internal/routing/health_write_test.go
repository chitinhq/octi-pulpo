package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteDriverHealthFile_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "new", "nested")

	hf := HealthFile{State: "CLOSED", Updated: time.Now().UTC().Format(time.RFC3339)}
	if err := WriteDriverHealthFile(subdir, "test-driver", hf); err != nil {
		t.Fatalf("WriteDriverHealthFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(subdir, "test-driver.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var got HealthFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.State != "CLOSED" {
		t.Errorf("state = %q, want CLOSED", got.State)
	}
}

func TestWriteDriverHealthFile_Atomic(t *testing.T) {
	// After a successful write there should be no leftover .tmp file.
	dir := t.TempDir()
	hf := HealthFile{State: "OPEN"}
	if err := WriteDriverHealthFile(dir, "driver-x", hf); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "driver-x.json.tmp")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("expected .tmp file to be absent after successful write")
	}
}

func TestMarkDriverOpen_NewFile(t *testing.T) {
	dir := t.TempDir()
	if err := MarkDriverOpen(dir, "claude-code"); err != nil {
		t.Fatalf("MarkDriverOpen: %v", err)
	}

	h := ReadDriverHealth(dir, "claude-code")
	if h.CircuitState != "OPEN" {
		t.Errorf("circuit state = %q, want OPEN", h.CircuitState)
	}
	if h.Failures != 1 {
		t.Errorf("failures = %d, want 1", h.Failures)
	}
	if h.OpenedAt == "" {
		t.Error("opened_at should be set")
	}
}

func TestMarkDriverOpen_IncrementsFailures(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED", Failures: 3})

	if err := MarkDriverOpen(dir, "copilot"); err != nil {
		t.Fatalf("MarkDriverOpen: %v", err)
	}

	h := ReadDriverHealth(dir, "copilot")
	if h.CircuitState != "OPEN" {
		t.Errorf("circuit state = %q, want OPEN", h.CircuitState)
	}
	if h.Failures != 4 {
		t.Errorf("failures = %d, want 4 (3+1)", h.Failures)
	}
}

func TestMarkDriverSuccess_ResetsClosed(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 5})

	if err := MarkDriverSuccess(dir, "copilot"); err != nil {
		t.Fatalf("MarkDriverSuccess: %v", err)
	}

	h := ReadDriverHealth(dir, "copilot")
	if h.CircuitState != "CLOSED" {
		t.Errorf("circuit state = %q, want CLOSED", h.CircuitState)
	}
	if h.Failures != 0 {
		t.Errorf("failures = %d, want 0", h.Failures)
	}
	if h.LastSuccess == "" {
		t.Error("last_success should be set after success")
	}
}

func TestMarkDriverSuccess_NoWriteWhenAlreadyClean(t *testing.T) {
	dir := t.TempDir()
	// Driver with no health file defaults to CLOSED/0 — MarkDriverSuccess should
	// skip the write entirely to avoid unnecessary disk I/O.
	if err := MarkDriverSuccess(dir, "goose"); err != nil {
		t.Fatalf("MarkDriverSuccess: %v", err)
	}
	// File should NOT have been created since the driver was already clean.
	if _, err := os.Stat(filepath.Join(dir, "goose.json")); !os.IsNotExist(err) {
		t.Error("expected no health file for already-clean driver")
	}
}

func TestRecommendAction(t *testing.T) {
	pct0 := 0
	pct50 := 50

	tests := []struct {
		name   string
		h      DriverHealth
		wantIn string // substring expected in result
	}{
		{
			name:   "healthy driver",
			h:      DriverHealth{CircuitState: "CLOSED"},
			wantIn: "healthy",
		},
		{
			name:   "closed but budget low",
			h:      DriverHealth{CircuitState: "CLOSED", BudgetPct: &pct0},
			wantIn: "budget low",
		},
		{
			name:   "open recent",
			h:      DriverHealth{CircuitState: "OPEN", OpenedAt: time.Now().UTC().Format(time.RFC3339)},
			wantIn: "waiting",
		},
		{
			name:   "open with zero budget",
			h:      DriverHealth{CircuitState: "OPEN", BudgetPct: &pct0, OpenedAt: time.Now().UTC().Format(time.RFC3339)},
			wantIn: "budget reset",
		},
		{
			name:   "half-open",
			h:      DriverHealth{CircuitState: "HALF"},
			wantIn: "half-open",
		},
		{
			name:   "healthy with good budget",
			h:      DriverHealth{CircuitState: "CLOSED", BudgetPct: &pct50},
			wantIn: "healthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RecommendAction(tt.h)
			if got == "" {
				t.Fatal("RecommendAction returned empty string")
			}
			// We just check that the string contains an expected substring,
			// not the exact wording (which may evolve).
			found := false
			for _, word := range splitWords(tt.wantIn) {
				if containsCI(got, word) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("RecommendAction(%+v) = %q, want substring %q", tt.h, got, tt.wantIn)
			}
		})
	}
}

// splitWords splits on space for multi-word hints.
func splitWords(s string) []string {
	var words []string
	w := ""
	for _, c := range s {
		if c == ' ' {
			if w != "" {
				words = append(words, w)
				w = ""
			}
		} else {
			w += string(c)
		}
	}
	if w != "" {
		words = append(words, w)
	}
	return words
}

func containsCI(s, sub string) bool {
	sl := toLower(s)
	subl := toLower(sub)
	return contains(sl, subl)
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
