package routing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HealthFile is the on-disk format of a driver health JSON file.
type HealthFile struct {
	State       string `json:"state"`        // CLOSED, OPEN, HALF
	Failures    int    `json:"failures"`
	LastFailure string `json:"last_failure"`
	LastSuccess string `json:"last_success"`
	OpenedAt    string `json:"opened_at"`
	ProbedAt    string `json:"probed_at"`
	Updated     string `json:"updated"`
}

// ReadDriverHealth reads a single driver health file and returns a DriverHealth.
func ReadDriverHealth(healthDir, driver string) DriverHealth {
	dh := DriverHealth{
		Name:         driver,
		CircuitState: "CLOSED", // default: assume healthy
	}

	path := filepath.Join(healthDir, driver+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return dh // file missing = healthy
	}

	var hf HealthFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return dh
	}

	if hf.State != "" {
		dh.CircuitState = hf.State
	}
	dh.Failures = hf.Failures
	dh.LastFailure = hf.LastFailure
	dh.LastSuccess = hf.LastSuccess
	dh.OpenedAt = hf.OpenedAt
	dh.LastSuccessAgo = humanAgo(hf.LastSuccess)
	dh.DaysSinceLastSuccess = daysSince(hf.LastSuccess)
	return dh
}

// daysSince returns whole-day count since an RFC3339 timestamp. Returns -1
// when the timestamp is empty or unparseable.
func daysSince(ts string) int {
	if ts == "" {
		return -1
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return -1
	}
	return int(time.Since(t).Hours() / 24)
}

// humanAgo returns a human-readable duration since the given RFC3339 timestamp,
// or an empty string if the timestamp is empty or unparseable.
func humanAgo(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// DiscoverDrivers lists all driver names from .json files in the health directory.
// Returns an empty slice if the directory doesn't exist.
func DiscoverDrivers(healthDir string) []string {
	entries, err := os.ReadDir(healthDir)
	if err != nil {
		return nil
	}

	var drivers []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".json") {
			drivers = append(drivers, strings.TrimSuffix(name, ".json"))
		}
	}
	return drivers
}

// ReadAllHealth reads health status for all discovered drivers.
func ReadAllHealth(healthDir string) []DriverHealth {
	drivers := DiscoverDrivers(healthDir)
	results := make([]DriverHealth, 0, len(drivers))
	for _, d := range drivers {
		results = append(results, ReadDriverHealth(healthDir, d))
	}
	return results
}

// WriteHealthFile atomically writes a driver health file to disk.
// Creates the directory if it does not exist.
func WriteHealthFile(healthDir, driver string, hf HealthFile) error {
	if err := os.MkdirAll(healthDir, 0755); err != nil {
		return fmt.Errorf("mkdir health dir: %w", err)
	}
	path := filepath.Join(healthDir, driver+".json")
	data, err := json.Marshal(hf)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// OpenCircuit marks a driver's circuit breaker as OPEN (exhausted/unreachable).
// Preserves existing failure count and last-success timestamp.
func OpenCircuit(healthDir, driver string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	existing := ReadDriverHealth(healthDir, driver)
	hf := HealthFile{
		State:       "OPEN",
		Failures:    existing.Failures + 1,
		LastFailure: now,
		LastSuccess: existing.LastSuccess,
		OpenedAt:    now,
		Updated:     now,
	}
	return WriteHealthFile(healthDir, driver, hf)
}

// CloseCircuit marks a driver's circuit breaker as CLOSED (healthy).
// Records a new last-success timestamp.
func CloseCircuit(healthDir, driver string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	existing := ReadDriverHealth(healthDir, driver)
	hf := HealthFile{
		State:       "CLOSED",
		Failures:    existing.Failures,
		LastFailure: existing.LastFailure,
		LastSuccess: now,
		ProbedAt:    now,
		Updated:     now,
	}
	return WriteHealthFile(healthDir, driver, hf)
}

// creditErrorPatterns are substrings (case-insensitive) that indicate a driver
// has exhausted its budget or been rate-limited.
var creditErrorPatterns = []string{
	"credit balance is too low",
	"no quota",
	"quota exceeded",
	"insufficient_quota",
	"exceeded your current quota",
	"you have exceeded",
	"rate limit exceeded",
	"429 too many requests",
	"rateLimitError",
}

// driverCreditKeywords maps specific error substrings to driver names.
// More specific patterns must come first.
// Ladder Forge II (2026-04-14): CLI-driver keywords (claude-code, copilot,
// codex, gemini) pruned. Surviving drivers (clawta, openclaw, gh-actions,
// anthropic/claude-api) have their own error surfaces; keep only the
// anthropic-API credit keywords here for claude-api budget exhaustion.
var driverCreditKeywords = []struct {
	keyword string
	driver  string
}{
	{"anthropic", "claude-api"},
	{"credit balance", "claude-api"},
}

// DetectExhaustedDriver scans agent output for known credit/quota error patterns.
// Returns the inferred driver name and true when a budget exhaustion error is found.
// Returns ("unknown", true) when an error is found but the driver cannot be identified.
func DetectExhaustedDriver(output string) (string, bool) {
	lower := strings.ToLower(output)
	for _, pattern := range creditErrorPatterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			for _, kw := range driverCreditKeywords {
				if strings.Contains(lower, strings.ToLower(kw.keyword)) {
					return kw.driver, true
				}
			}
			return "unknown", true
		}
	}
	return "", false
}
