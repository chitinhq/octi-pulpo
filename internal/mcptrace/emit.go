// Package mcptrace emits MCP tool invocation events to a JSONL file so
// Sentinel can ingest and analyse which tools agents actually use.
//
// The event schema matches chitinhq/chitin's governance events.jsonl so the
// existing Sentinel chitin_governance ingester picks them up with no change:
// ts, sid, agent, tool, action, outcome, reason, source, latency_us.
//
// Destination:
//   1. $MCPTRACE_FILE if set (absolute path)
//   2. $CHITIN_WORKSPACE/.chitin/events.jsonl if CHITIN_WORKSPACE is set
//   3. $HOME/.chitin/mcp_events.jsonl as a fallback
//   4. no-op if none of the above resolve
package mcptrace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one MCP tool invocation.
type Event struct {
	Timestamp string `json:"ts"`
	SessionID string `json:"sid,omitempty"`
	Agent     string `json:"agent"`
	Tool      string `json:"tool"`
	Action    string `json:"action"`         // always "mcp_call" for MCP server events
	Outcome   string `json:"outcome"`        // "allow" | "deny" (allow = success, deny = error)
	Reason    string `json:"reason,omitempty"`
	Source    string `json:"source"`         // the MCP server name, e.g. "octi" or "atlas"
	LatencyUs int64  `json:"latency_us"`
}

var (
	writeMu sync.Mutex
)

// Emit appends a single event to the configured JSONL file.
// Never blocks the caller on errors — telemetry is best-effort.
func Emit(source, agent, tool, outcome, reason string, start time.Time) {
	path := destination()
	if path == "" {
		return
	}

	ev := Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		SessionID: os.Getenv("CHITIN_SESSION_ID"),
		Agent:     agent,
		Tool:      "mcp__" + source + "__" + tool,
		Action:    "mcp_call",
		Outcome:   outcome,
		Reason:    reason,
		Source:    source,
		LatencyUs: time.Since(start).Microseconds(),
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	data = append(data, '\n')

	writeMu.Lock()
	defer writeMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(data)
}

// destination resolves the JSONL path from environment, or returns ""
// to disable emission when nothing is configured.
func destination() string {
	if p := os.Getenv("MCPTRACE_FILE"); p != "" {
		return p
	}
	if ws := os.Getenv("CHITIN_WORKSPACE"); ws != "" {
		return filepath.Join(ws, ".chitin", "events.jsonl")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".chitin", "mcp_events.jsonl")
	}
	return ""
}
