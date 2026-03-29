package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	return dh
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
