// Package swarmcircuit subscribes to circuit.<signal> events on the
// shared chitin events.jsonl stream and exposes a paused/resumed state
// to the dispatcher. The patrol that produces these events lives in
// chitinhq/sentinel (internal/circuit) — this package is the consumer.
//
// Architecture: orthogonal patrol, never inline. Sentinel's patrol
// detects a swarm-wide condition and emits a flow_failed event named
// `circuit.<signal>` (e.g. `circuit.retry_storm`). Octi's dispatcher
// tails events.jsonl, sees the event, sets a pause flag, and stops
// admitting new dispatches until a `circuit.reset` event clears it.
//
// Distinct from the per-driver health circuit in internal/routing —
// that one fails individual drivers based on Octi's own dispatch
// outcomes. This swarm circuit reflects fleet-wide conditions read
// from sentinel telemetry.
package swarmcircuit

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// State is a snapshot of the swarm circuit. Safe to JSON-marshal into
// the dispatch_status response.
type State struct {
	Paused      bool   `json:"paused"`
	Signal      string `json:"signal,omitempty"`       // e.g. "retry_storm"
	Reason      string `json:"reason,omitempty"`       // human-readable threshold + sample
	Since       string `json:"paused_since,omitempty"` // RFC3339
	LastEvent   string `json:"last_event,omitempty"`   // last circuit.* tool name observed
	LastEventAt string `json:"last_event_at,omitempty"`
}

// Subscriber tails a JSONL stream and maintains pause state. The zero
// value is unusable — build with New.
type Subscriber struct {
	path   string
	logger *log.Logger

	state atomic.Value // *State, never nil after construction
	mu    sync.Mutex   // serializes apply()
}

// New returns a subscriber bound to path. If path is empty,
// DefaultPath() is consulted. Logger may be nil.
func New(path string, logger *log.Logger) *Subscriber {
	if path == "" {
		path = DefaultPath()
	}
	s := &Subscriber{path: path, logger: logger}
	s.state.Store(&State{})
	return s
}

// DefaultPath mirrors octi/internal/flow.destination() so the
// subscriber reads the same file the producer writes:
//  1. $MCPTRACE_FILE
//  2. $CHITIN_WORKSPACE/.chitin/events.jsonl
//  3. $HOME/.chitin/flow_events.jsonl
func DefaultPath() string {
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

// Snapshot returns the current state by value.
func (s *Subscriber) Snapshot() State {
	if s == nil {
		return State{}
	}
	st := s.state.Load().(*State)
	return *st
}

// Paused is the hot-path check used by the dispatcher.
func (s *Subscriber) Paused() bool {
	if s == nil {
		return false
	}
	return s.state.Load().(*State).Paused
}

// Reset clears pause state. Idempotent. Used by mcp__octi__circuit_reset
// (when scope=="swarm") and by an observed `circuit.reset` event.
func (s *Subscriber) Reset(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.state.Load().(*State)
	next := &State{
		LastEvent:   "circuit.reset",
		LastEventAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.state.Store(next)
	if s.logger != nil && prev.Paused {
		s.logger.Printf("swarm-circuit: RESET (was %s) reason=%q", prev.Signal, reason)
	}
}

// Run tails the JSONL file and applies events. It blocks until ctx is
// done. A missing file is not an error — Run polls for it to appear.
// Truncation/rotation is handled by re-opening on EOF when size shrinks.
func (s *Subscriber) Run(ctx context.Context) error {
	if s.path == "" {
		if s.logger != nil {
			s.logger.Println("swarm-circuit: no events path resolved; subscriber idle")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	if s.logger != nil {
		s.logger.Printf("swarm-circuit: tailing %s", s.path)
	}

	var (
		f      *os.File
		reader *bufio.Reader
		offset int64
	)
	openFile := func() error {
		if f != nil {
			f.Close()
		}
		var err error
		f, err = os.Open(s.path)
		if err != nil {
			return err
		}
		// Start from the end so we don't replay history at startup —
		// historical trips are stale by definition (operator already
		// reset, or the situation has cleared).
		end, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		offset = end
		reader = bufio.NewReader(f)
		return nil
	}

	for {
		if reader == nil {
			if err := openFile(); err != nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
					continue
				}
			}
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			s.handleLine(line)
		}
		if err == io.EOF {
			// Detect rotation/truncation: if the file is shorter than
			// our offset, re-open from end.
			if fi, statErr := os.Stat(s.path); statErr == nil && fi.Size() < offset {
				reader = nil
				continue
			}
			if pos, posErr := f.Seek(0, io.SeekCurrent); posErr == nil {
				offset = pos
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			// Read error — close, sleep, retry.
			reader = nil
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
}

// handleLine parses a single JSONL line and applies it. Best-effort:
// malformed lines are ignored.
func (s *Subscriber) handleLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var ev struct {
		Tool   string                 `json:"tool"`
		Action string                 `json:"action"`
		Fields map[string]interface{} `json:"fields"`
		Ts     string                 `json:"ts"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}
	tool := strings.TrimPrefix(ev.Tool, "flow.")
	if !strings.HasPrefix(tool, "circuit.") {
		return
	}
	signal := strings.TrimPrefix(tool, "circuit.")

	if signal == "reset" {
		s.Reset("event circuit.reset")
		return
	}

	// Anything else under circuit.* is a trip. The producer uses
	// flow_failed for trips, but we don't gate on action — any
	// circuit.<signal> we see is treated as a trip.
	s.applyTrip(signal, ev.Fields, ev.Ts)
}

func (s *Subscriber) applyTrip(signal string, fields map[string]interface{}, ts string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.state.Load().(*State)
	if prev.Paused && prev.Signal == signal {
		// Already paused on this signal — refresh last-event timestamp
		// but don't re-log.
		prev.LastEvent = "circuit." + signal
		prev.LastEventAt = ts
		return
	}
	reason := signal
	if t, ok := fields["threshold"].(string); ok && t != "" {
		reason = signal + ": " + t
	}
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339)
	}
	next := &State{
		Paused:      true,
		Signal:      signal,
		Reason:      reason,
		Since:       ts,
		LastEvent:   "circuit." + signal,
		LastEventAt: ts,
	}
	s.state.Store(next)
	if s.logger != nil {
		s.logger.Printf("swarm-circuit: PAUSED signal=%s reason=%q", signal, reason)
	}
}
