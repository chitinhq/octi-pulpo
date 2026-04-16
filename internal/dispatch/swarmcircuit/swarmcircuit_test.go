package swarmcircuit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestSubscriberPausesOnTripAndResumesOnReset emits a synthetic
// circuit.retry_storm event and then a circuit.reset, asserting the
// subscriber transitions accordingly.
func TestSubscriberPausesOnTripAndResumesOnReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}

	sub := New(path, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sub.Run(ctx)

	// Give Run() a beat to open + seek to end.
	time.Sleep(50 * time.Millisecond)

	if sub.Paused() {
		t.Fatalf("subscriber paused before any trip event")
	}

	writeLine(t, path, `{"ts":"2026-04-15T20:00:00Z","tool":"flow.circuit.retry_storm","action":"flow_failed","fields":{"threshold":"retries>3 within 1h","signal":"retry_storm"}}`)

	if !waitFor(func() bool { return sub.Paused() }, time.Second) {
		t.Fatalf("subscriber did not pause within 1s; state=%+v", sub.Snapshot())
	}
	st := sub.Snapshot()
	if st.Signal != "retry_storm" {
		t.Fatalf("expected signal retry_storm, got %q", st.Signal)
	}
	if st.Reason == "" {
		t.Fatalf("expected reason populated, got empty")
	}

	writeLine(t, path, `{"ts":"2026-04-15T20:01:00Z","tool":"flow.circuit.reset","action":"flow_completed"}`)

	if !waitFor(func() bool { return !sub.Paused() }, time.Second) {
		t.Fatalf("subscriber did not resume within 1s; state=%+v", sub.Snapshot())
	}
}

func TestSubscriberIgnoresUnrelatedEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	sub := New(path, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sub.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	writeLine(t, path, `{"tool":"flow.swarm.dispatch","action":"flow_completed"}`)
	writeLine(t, path, `garbage not json`)
	writeLine(t, path, `{"tool":"flow.unrelated","action":"flow_failed"}`)

	time.Sleep(150 * time.Millisecond)
	if sub.Paused() {
		t.Fatalf("subscriber paused on unrelated event: %+v", sub.Snapshot())
	}
}

func TestResetIsIdempotent(t *testing.T) {
	sub := New("/dev/null/missing", nil)
	sub.Reset("test")
	sub.Reset("test")
	if sub.Paused() {
		t.Fatalf("paused after reset")
	}
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
