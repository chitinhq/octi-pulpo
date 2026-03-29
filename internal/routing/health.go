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
	return dh
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
