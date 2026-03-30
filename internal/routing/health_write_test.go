package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteHealthFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	hf := HealthFile{
		State:       "OPEN",
		Failures:    3,
		LastFailure: "2026-03-29T10:00:00Z",
		LastSuccess: "2026-03-28T08:00:00Z",
		OpenedAt:    "2026-03-29T10:00:00Z",
		Updated:     "2026-03-29T10:00:00Z",
	}

	if err := WriteHealthFile(dir, "claude-code", hf); err != nil {
		t.Fatalf("WriteHealthFile: %v", err)
	}

	got := ReadDriverHealth(dir, "claude-code")
	if got.CircuitState != "OPEN" {
		t.Errorf("state: got %q, want OPEN", got.CircuitState)
	}
	if got.Failures != 3 {
		t.Errorf("failures: got %d, want 3", got.Failures)
	}
	if got.LastSuccess != "2026-03-28T08:00:00Z" {
		t.Errorf("last_success: got %q", got.LastSuccess)
	}
}

func TestWriteHealthFile_CreatesDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "health")

	hf := HealthFile{State: "CLOSED"}
	if err := WriteHealthFile(dir, "copilot", hf); err != nil {
		t.Fatalf("WriteHealthFile on missing dir: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "copilot.json")); err != nil {
		t.Fatalf("health file not created: %v", err)
	}
}

func TestOpenCircuit(t *testing.T) {
	dir := t.TempDir()
	// Seed with an existing health file so we can verify failure count increments
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 2, LastSuccess: "2026-03-28T08:00:00Z"})

	if err := OpenCircuit(dir, "claude-code"); err != nil {
		t.Fatalf("OpenCircuit: %v", err)
	}

	got := ReadDriverHealth(dir, "claude-code")
	if got.CircuitState != "OPEN" {
		t.Errorf("state: got %q, want OPEN", got.CircuitState)
	}
	if got.Failures != 3 {
		t.Errorf("failures: got %d, want 3 (incremented from 2)", got.Failures)
	}
	// LastSuccess must be preserved
	if got.LastSuccess != "2026-03-28T08:00:00Z" {
		t.Errorf("last_success changed unexpectedly: %q", got.LastSuccess)
	}
	// LastFailure must be set to roughly now
	if got.LastFailure == "" {
		t.Error("last_failure not set after OpenCircuit")
	}
}

func TestCloseCircuit(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 5, LastFailure: "2026-03-29T10:00:00Z"})

	if err := CloseCircuit(dir, "copilot"); err != nil {
		t.Fatalf("CloseCircuit: %v", err)
	}

	got := ReadDriverHealth(dir, "copilot")
	if got.CircuitState != "CLOSED" {
		t.Errorf("state: got %q, want CLOSED", got.CircuitState)
	}
	// Failure count is preserved (not reset — avoids hiding history)
	if got.Failures != 5 {
		t.Errorf("failures: got %d, want 5", got.Failures)
	}
	// LastSuccess must be set to a recent timestamp
	if got.LastSuccess == "" {
		t.Error("last_success not set after CloseCircuit")
	}
	ts, err := time.Parse(time.RFC3339, got.LastSuccess)
	if err != nil {
		t.Fatalf("last_success not valid RFC3339: %v", err)
	}
	if time.Since(ts) > 5*time.Second {
		t.Errorf("last_success is too old: %s", got.LastSuccess)
	}
}

func TestForceCloseCircuit_ResetsOpenCircuit(t *testing.T) {
	dir := t.TempDir()
	// Seed an OPEN circuit with a high failure count.
	writeHealth(t, dir, "codex", HealthFile{State: "OPEN", Failures: 73, LastFailure: "2026-03-30T00:57:06Z"})

	if err := ForceCloseCircuit(dir, "codex"); err != nil {
		t.Fatalf("ForceCloseCircuit: %v", err)
	}

	h := ReadDriverHealth(dir, "codex")
	if h.CircuitState != "CLOSED" {
		t.Errorf("circuit state = %q, want CLOSED", h.CircuitState)
	}
	if h.Failures != 0 {
		t.Errorf("failures = %d, want 0 (force reset clears count)", h.Failures)
	}
	if h.LastSuccess == "" {
		t.Error("last_success should be set after ForceCloseCircuit")
	}
	// last_failure must be preserved for audit history.
	if h.LastFailure != "2026-03-30T00:57:06Z" {
		t.Errorf("last_failure = %q, want preserved from before reset", h.LastFailure)
	}
}

func TestForceCloseCircuit_AlwaysWrites(t *testing.T) {
	// ForceCloseCircuit should write even when the circuit is already CLOSED
	// (unlike MarkDriverSuccess which skips the write when already clean).
	dir := t.TempDir()
	writeHealth(t, dir, "gemini", HealthFile{State: "CLOSED", Failures: 0})

	before := ReadDriverHealth(dir, "gemini")
	if err := ForceCloseCircuit(dir, "gemini"); err != nil {
		t.Fatalf("ForceCloseCircuit: %v", err)
	}
	after := ReadDriverHealth(dir, "gemini")
	// last_success should have been updated even for an already-CLOSED circuit.
	if after.LastSuccess == before.LastSuccess {
		t.Error("expected last_success to be updated by ForceCloseCircuit")
	}
}

func TestDetectExhaustedDriver_ClaudeCredit(t *testing.T) {
	output := "Error: Credit balance is too low. Visit claude.ai to top up."
	driver, found := DetectExhaustedDriver(output)
	if !found {
		t.Fatal("expected credit error to be detected")
	}
	if driver != "claude-code" {
		t.Errorf("driver: got %q, want claude-code", driver)
	}
}

func TestDetectExhaustedDriver_QuotaExceeded(t *testing.T) {
	output := "openai.com: You have exceeded your current quota, please check your plan."
	driver, found := DetectExhaustedDriver(output)
	if !found {
		t.Fatal("expected quota error to be detected")
	}
	if driver != "codex" {
		t.Errorf("driver: got %q, want codex", driver)
	}
}

func TestDetectExhaustedDriver_429(t *testing.T) {
	output := "HTTP/1.1 429 Too Many Requests\nRetry-After: 60"
	driver, found := DetectExhaustedDriver(output)
	if !found {
		t.Fatal("expected 429 error to be detected")
	}
	// Driver can't be inferred from a bare 429 without more context
	if driver == "" {
		t.Error("driver should be 'unknown', not empty")
	}
}

func TestDetectExhaustedDriver_NoError(t *testing.T) {
	output := "Successfully completed review. 3 comments posted."
	driver, found := DetectExhaustedDriver(output)
	if found {
		t.Errorf("false positive: detected driver %q on clean output", driver)
	}
}

func TestDetectExhaustedDriver_CaseInsensitive(t *testing.T) {
	output := strings.ToUpper("credit balance is too low — please top up your account")
	_, found := DetectExhaustedDriver(output)
	if !found {
		t.Fatal("expected case-insensitive match for credit balance error")
	}
}

// --- Tests for WriteDriverHealthFile, MarkDriverOpen, MarkDriverSuccess, RecommendAction ---

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
			// Check that the string contains an expected substring.
			found := false
			for _, word := range hwSplitWords(tt.wantIn) {
				if hwContainsCI(got, word) {
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

// hwSplitWords splits on space for multi-word hints.
func hwSplitWords(s string) []string {
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

func hwContainsCI(s, sub string) bool {
	return hwContains(hwToLower(s), hwToLower(sub))
}

func hwToLower(s string) string {
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

func hwContains(s, sub string) bool {
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
