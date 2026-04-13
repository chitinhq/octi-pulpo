// Package flow emits flow-lifecycle events to a JSONL file so Sentinel can
// observe swarm health across the octi dispatch path.
//
// The wire format mirrors sentinel/internal/flow and is compatible with the
// chitin governance events.jsonl ingester: ts, sid, agent, tool, action,
// outcome, source, latency_us, fields.
//
// Destination resolution (same as internal/mcptrace):
//  1. $MCPTRACE_FILE if set
//  2. $CHITIN_WORKSPACE/.chitin/events.jsonl if CHITIN_WORKSPACE is set
//  3. $HOME/.chitin/flow_events.jsonl as a fallback
//  4. no-op if none of the above resolve
package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Status values for flow events.
const (
	StatusStarted   = "started"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Event is one flow-lifecycle record.
type Event struct {
	Timestamp string                 `json:"ts"`
	SessionID string                 `json:"sid,omitempty"`
	Agent     string                 `json:"agent"`
	Tool      string                 `json:"tool"`       // "flow.<name>"
	Action    string                 `json:"action"`     // "flow_started" | "flow_completed" | "flow_failed"
	Outcome   string                 `json:"outcome"`    // "allow" on start/complete, "deny" on fail
	Source    string                 `json:"source"`     // always "flow"
	LatencyUs int64                  `json:"latency_us"` // 0 for start/fail, duration for complete
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

var writeMu sync.Mutex

// Emit appends a single flow event. Best-effort; errors are swallowed so
// telemetry never breaks the caller.
func Emit(name, status string, fields map[string]interface{}) {
	EmitLatency(name, status, fields, 0)
}

// EmitLatency is Emit with an explicit latency in microseconds (for Complete/Fail).
func EmitLatency(name, status string, fields map[string]interface{}, latencyUs int64) {
	path := destination()
	if path == "" {
		return
	}

	outcome := "allow"
	if status == StatusFailed {
		outcome = "deny"
	}

	ev := Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		SessionID: os.Getenv("CHITIN_SESSION_ID"),
		Agent:     agentName(),
		Tool:      "flow." + name,
		Action:    "flow_" + status,
		Outcome:   outcome,
		Source:    "flow",
		LatencyUs: latencyUs,
		Fields:    fields,
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	data = append(data, '\n')

	writeMu.Lock()
	defer writeMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

// Start emits a flow_started event.
func Start(name string, fields map[string]interface{}) {
	Emit(name, StatusStarted, fields)
}

// Complete emits a flow_completed event with measured latency from start.
func Complete(name string, start time.Time, fields map[string]interface{}) {
	EmitLatency(name, StatusCompleted, fields, time.Since(start).Microseconds())
}

// Fail emits a flow_failed event. Latency is measured from start; err is
// attached in fields["error"] if non-nil.
func Fail(name string, start time.Time, err error, fields map[string]interface{}) {
	if err != nil {
		if fields == nil {
			fields = map[string]interface{}{}
		}
		fields["error"] = err.Error()
	}
	EmitLatency(name, StatusFailed, fields, time.Since(start).Microseconds())
}

// Span is a helper that emits Start immediately and returns a function to
// call on scope exit. The returned function picks Complete or Fail based on
// the pointer to error it's given.
//
//	defer flow.Span("swarm.dispatch", nil)(&err)
func Span(name string, fields map[string]interface{}) func(*error) {
	start := time.Now()
	Start(name, fields)
	return func(errp *error) {
		if errp != nil && *errp != nil {
			Fail(name, start, *errp, fields)
			return
		}
		Complete(name, start, fields)
	}
}

func agentName() string {
	if a := os.Getenv("CHITIN_AGENT_NAME"); a != "" {
		return a
	}
	return "octi-pulpo"
}

func destination() string {
	if p := os.Getenv("MCPTRACE_FILE"); p != "" {
		return p
	}
	if ws := os.Getenv("CHITIN_WORKSPACE"); ws != "" {
		return filepath.Join(ws, ".chitin", "events.jsonl")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".chitin", "flow_events.jsonl")
	}
	return ""
}
