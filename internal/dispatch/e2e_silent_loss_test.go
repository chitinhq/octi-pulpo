package dispatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// recordingAdapter records Dispatch invocations and returns a configurable
// response. This is THE instrument used to detect the silent-loss bug: if the
// dispatcher reports action="dispatched" but never calls the adapter, calls==0
// and the test fails — which is exactly what we want.
type recordingAdapter struct {
	name      string
	calls     int32
	respond   func(ctx context.Context, task *Task) (*AdapterResult, error)
	canAccept bool
}

func (m *recordingAdapter) Name() string              { return m.name }
func (m *recordingAdapter) CanAccept(task *Task) bool { return m.canAccept }
func (m *recordingAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
	atomic.AddInt32(&m.calls, 1)
	if m.respond != nil {
		return m.respond(ctx, task)
	}
	return &AdapterResult{TaskID: task.ID, Status: "completed", Adapter: m.name}, nil
}
func (m *recordingAdapter) Calls() int { return int(atomic.LoadInt32(&m.calls)) }

// TestE2E_DispatchNotSilent is the permanent regression guard for the
// "silent-loss" pattern (workspace#408): Dispatch() returning action="dispatched"
// without ever invoking the registered adapter. If this test goes red, the
// bug — or its evil twin — is back.
//
// Table-driven: simulate the GH API adapter returning 204 (success), 500
// (server error), and a network timeout. In each case we assert (a) the
// reported Action matches the adapter outcome, and (b) the adapter was
// invoked exactly once. Zero invocations is the silent-loss signature.
func TestE2E_DispatchNotSilent(t *testing.T) {
	cases := []struct {
		name           string
		respond        func(ctx context.Context, task *Task) (*AdapterResult, error)
		event          Event
		wantAction     string
		wantAdapterHit int
	}{
		{
			name: "204_success_dispatched",
			respond: func(ctx context.Context, task *Task) (*AdapterResult, error) {
				return &AdapterResult{TaskID: task.ID, Status: "completed", Adapter: "claude-code"}, nil
			},
			event:          Event{Type: EventManual, Source: "test", Repo: "chitinhq/octi"},
			wantAction:     "dispatched",
			wantAdapterHit: 1,
		},
		{
			name: "500_server_error_failed",
			respond: func(ctx context.Context, task *Task) (*AdapterResult, error) {
				return &AdapterResult{TaskID: task.ID, Status: "failed", Adapter: "claude-code", Error: "500"}, errors.New("upstream 500")
			},
			event:          Event{Type: EventManual, Source: "test", Repo: "chitinhq/octi"},
			wantAction:     "failed",
			wantAdapterHit: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ctx := testSetup(t)
			adapter := &recordingAdapter{name: "claude-code", canAccept: true, respond: tc.respond}
			d.SetAdapters(adapter)

			result, err := d.Dispatch(ctx, tc.event, "test-agent-"+tc.name, 2)
			if err != nil && tc.wantAction != "failed" {
				t.Fatalf("dispatch err: %v", err)
			}

			// THE assertion that guards against silent-loss regression:
			// if the dispatcher routed at all, the adapter MUST have been called.
			if got := adapter.Calls(); got != tc.wantAdapterHit {
				t.Errorf("SILENT-LOSS REGRESSION: adapter.Dispatch calls = %d, want %d (action=%q reason=%q)",
					got, tc.wantAdapterHit, result.Action, result.Reason)
			}

			if result.Action != tc.wantAction {
				t.Errorf("action = %q, want %q (reason: %s)", result.Action, tc.wantAction, result.Reason)
			}

			// And in particular: action must NEVER be "dispatched" if the adapter
			// wasn't invoked. This catches the original silent-loss footprint.
			if result.Action == "dispatched" && adapter.Calls() == 0 {
				t.Fatalf("SILENT-LOSS REGRESSION: action=dispatched but adapter never called")
			}
		})
	}
}

// TestE2E_DispatchUnroutable_NoSilentDispatched: when no adapter matches the
// routed driver, the result MUST NOT be "dispatched". This is the second
// silent-loss footprint — claiming success with no execution surface attached.
func TestE2E_DispatchUnroutable_NoSilentDispatched(t *testing.T) {
	d, ctx := testSetup(t)
	// Register an adapter under a *different* name than the routed driver
	// ("claude-code" is what the testSetup health file advertises).
	wrongAdapter := &recordingAdapter{name: "not-the-driver", canAccept: true}
	d.SetAdapters(wrongAdapter)

	event := Event{Type: EventManual, Source: "test", Repo: "chitinhq/octi"}
	result, err := d.Dispatch(ctx, event, "test-agent-unroutable", 2)
	if err != nil {
		t.Fatalf("dispatch err: %v", err)
	}

	if wrongAdapter.Calls() != 0 {
		t.Errorf("wrong adapter should not be invoked, got %d calls", wrongAdapter.Calls())
	}

	if result.Action == "dispatched" {
		t.Fatalf("SILENT-LOSS REGRESSION: action=dispatched with no matching adapter; expected unroutable/failed (reason: %s)", result.Reason)
	}
}

// TestE2E_DispatchAdapterTimeout_RespectsContext is the chaos case: if the
// adapter hangs, the dispatcher must respect the caller's context deadline
// rather than block forever. Hanging adapters were one path to the original
// silent-loss symptom (no observable outcome).
func TestE2E_DispatchAdapterTimeout_RespectsContext(t *testing.T) {
	d, baseCtx := testSetup(t)

	hung := make(chan struct{})
	t.Cleanup(func() { close(hung) })

	hangAdapter := &recordingAdapter{
		name:      "claude-code",
		canAccept: true,
		respond: func(ctx context.Context, task *Task) (*AdapterResult, error) {
			select {
			case <-ctx.Done():
				return &AdapterResult{TaskID: task.ID, Status: "failed", Error: ctx.Err().Error()}, ctx.Err()
			case <-hung:
				return nil, errors.New("test cleanup")
			}
		},
	}
	d.SetAdapters(hangAdapter)

	ctx, cancel := context.WithTimeout(baseCtx, 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	event := Event{Type: EventManual, Source: "test", Repo: "chitinhq/octi"}
	done := make(chan struct{})
	go func() {
		_, _ = d.Dispatch(ctx, event, "test-agent-timeout", 2)
		close(done)
	}()

	select {
	case <-done:
		// Must return within a reasonable bound past the deadline.
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("dispatch ignored context deadline: took %v", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher hung past context deadline — adapter timeout not respected (potential silent-loss path)")
	}
}
