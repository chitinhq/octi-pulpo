package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// WriteDriverHealthFile atomically writes a driver health JSON file.
// It writes to a temp file first and then renames to avoid torn reads by
// the bash worker scripts that also read/write the same directory.
func WriteDriverHealthFile(healthDir, driver string, hf HealthFile) error {
	if err := os.MkdirAll(healthDir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(hf, "", "  ")
	if err != nil {
		return err
	}

	// Write to a sibling temp file then atomically rename.
	path := filepath.Join(healthDir, driver+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MarkDriverOpen marks a driver circuit as OPEN (credit/quota exhausted or
// repeated failures). Safe to call when no health file exists yet.
func MarkDriverOpen(healthDir, driver string) error {
	existing := ReadDriverHealth(healthDir, driver)
	now := time.Now().UTC().Format(time.RFC3339)
	hf := HealthFile{
		State:       "OPEN",
		Failures:    existing.Failures + 1,
		LastFailure: now,
		LastSuccess: existing.LastSuccess,
		OpenedAt:    now,
		Updated:     now,
	}
	return WriteDriverHealthFile(healthDir, driver, hf)
}

// ForceCloseCircuit manually resets a driver circuit to CLOSED with zero failures.
// Unlike MarkDriverSuccess, this always writes even if the circuit is already
// CLOSED, making it suitable for operator overrides. The probed_at field is set
// to the current time to record when the manual reset occurred.
func ForceCloseCircuit(healthDir, driver string) error {
	existing := ReadDriverHealth(healthDir, driver)
	now := time.Now().UTC().Format(time.RFC3339)
	hf := HealthFile{
		State:       "CLOSED",
		Failures:    0,
		LastFailure: existing.LastFailure,
		LastSuccess: now,
		OpenedAt:    "",
		ProbedAt:    now,
		Updated:     now,
	}
	return WriteDriverHealthFile(healthDir, driver, hf)
}

// MarkDriverSuccess resets a driver circuit to CLOSED after a successful run.
// Skips the write when the driver is already CLOSED with zero failures to
// avoid unnecessary disk I/O on every healthy run.
func MarkDriverSuccess(healthDir, driver string) error {
	existing := ReadDriverHealth(healthDir, driver)
	if existing.CircuitState == "CLOSED" && existing.Failures == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	hf := HealthFile{
		State:       "CLOSED",
		Failures:    0,
		LastFailure: existing.LastFailure,
		LastSuccess: now,
		Updated:     now,
	}
	return WriteDriverHealthFile(healthDir, driver, hf)
}
