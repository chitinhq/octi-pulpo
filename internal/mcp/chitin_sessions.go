package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"
)

// loadChitinSessionSnapshot returns the currently-active chitin session
// and recent history as a JSON-decodable payload, or nil if chitin is
// unreachable. It shells out to `chitin session status --format json
// --recent N` so there's no compile-time dependency between octi and
// chitin — we re-use whatever chitin is on PATH.
//
// Keeping this purely additive (nil on failure) means dispatch_status
// stays backward-compatible and never breaks because of a separate
// binary being absent or stale.
func loadChitinSessionSnapshot(parent context.Context) map[string]any {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "chitin", "session", "status",
		"--format", "json", "--recent", "10")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var snap map[string]any
	if err := json.Unmarshal(out, &snap); err != nil {
		return nil
	}
	return snap
}
