package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// --- minimal Redis mock implementing redis.Cmdable ---

// mockRedis is a minimal in-memory implementation of the redis.Cmdable interface
// for the subset of commands used by CopilotFixLoop (Incr, Del).
//
// redis.Cmdable is a large interface; we implement it by embedding a
// *redis.Client (nil) and overriding just the methods we use. Any call to an
// unimplemented method will panic, which is fine for tests — it means we're
// calling something unexpected.
type mockRedis struct {
	mu      sync.Mutex
	counters map[string]int64
}

func newMockRedis() *mockRedis {
	return &mockRedis{counters: make(map[string]int64)}
}

func (m *mockRedis) get(key string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[key]
}

// Incr increments the key and returns an *redis.IntCmd with the new value.
func (m *mockRedis) Incr(ctx context.Context, key string) *redis.IntCmd {
	m.mu.Lock()
	m.counters[key]++
	val := m.counters[key]
	m.mu.Unlock()

	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(val)
	return cmd
}

// Del deletes the given keys and returns an *redis.IntCmd.
func (m *mockRedis) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	m.mu.Lock()
	var deleted int64
	for _, k := range keys {
		if _, ok := m.counters[k]; ok {
			delete(m.counters, k)
			deleted++
		}
	}
	m.mu.Unlock()

	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(deleted)
	return cmd
}

// newCopilotFixLoopForTest constructs a CopilotFixLoop pointing at the given
// test server URL so we can capture GitHub API calls.
func newCopilotFixLoopForTest(ghToken string, rdb *mockRedis, baseURL string) *CopilotFixLoop {
	return &CopilotFixLoop{
		ghToken:     ghToken,
		rdb:         rdb,
		baseURL:     baseURL,
		maxAttempts: defaultMaxAttempts,
	}
}

// --- helpers ---

// capturedComment records the JSON body sent to the GitHub API mock.
type capturedComment struct {
	Body string `json:"body"`
}

// newGHMock returns a test server that records PR comment POST requests.
func newGHMock(t *testing.T) (*httptest.Server, *[]capturedComment) {
	t.Helper()
	var comments []capturedComment
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		var c capturedComment
		if err := json.Unmarshal(body, &c); err != nil {
			t.Errorf("unmarshal body: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		comments = append(comments, c)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &comments
}

// --- tests ---

// TestCopilotFix_ChangesRequested_PostsComment verifies that a changes_requested
// review causes the @copilot trigger comment to be posted.
func TestCopilotFix_ChangesRequested_PostsComment(t *testing.T) {
	srv, comments := newGHMock(t)
	rdb := newMockRedis()
	fix := newCopilotFixLoopForTest("tok", rdb, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := fix.HandleReview(ctx, "owner/repo", 42, "changes_requested"); err != nil {
		t.Fatalf("HandleReview: %v", err)
	}

	if len(*comments) != 1 {
		t.Fatalf("expected 1 comment posted, got %d", len(*comments))
	}
	if (*comments)[0].Body != copilotFixComment {
		t.Errorf("expected @copilot comment, got %q", (*comments)[0].Body)
	}
}

// TestCopilotFix_ChangesRequested_IncrementsCounter verifies the Redis counter
// is incremented on each changes_requested review.
func TestCopilotFix_ChangesRequested_IncrementsCounter(t *testing.T) {
	srv, _ := newGHMock(t)
	rdb := newMockRedis()
	fix := newCopilotFixLoopForTest("tok", rdb, srv.URL)

	ctx := context.Background()
	key := fix.attemptKey("owner/repo", 7)

	_ = fix.HandleReview(ctx, "owner/repo", 7, "changes_requested")
	if got := rdb.get(key); got != 1 {
		t.Errorf("after 1st review: counter=%d, want 1", got)
	}

	_ = fix.HandleReview(ctx, "owner/repo", 7, "changes_requested")
	if got := rdb.get(key); got != 2 {
		t.Errorf("after 2nd review: counter=%d, want 2", got)
	}
}

// TestCopilotFix_Escalation verifies that after maxAttempts the escalation
// comment is posted instead of the @copilot trigger.
func TestCopilotFix_Escalation(t *testing.T) {
	srv, comments := newGHMock(t)
	rdb := newMockRedis()
	fix := newCopilotFixLoopForTest("tok", rdb, srv.URL)

	ctx := context.Background()

	// First attempt: @copilot comment
	if err := fix.HandleReview(ctx, "owner/repo", 10, "changes_requested"); err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	// Second attempt (== maxAttempts): escalation comment
	if err := fix.HandleReview(ctx, "owner/repo", 10, "changes_requested"); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	// Third attempt (> maxAttempts): no comment (counter already above max)
	if err := fix.HandleReview(ctx, "owner/repo", 10, "changes_requested"); err != nil {
		t.Fatalf("attempt 3: %v", err)
	}

	if len(*comments) != 2 {
		t.Fatalf("expected 2 comments total, got %d", len(*comments))
	}
	if (*comments)[0].Body != copilotFixComment {
		t.Errorf("attempt 1: expected @copilot comment, got %q", (*comments)[0].Body)
	}
	if (*comments)[1].Body != copilotEscalComment {
		t.Errorf("attempt 2: expected escalation comment, got %q", (*comments)[1].Body)
	}
}

// TestCopilotFix_Approved_ResetsCounter verifies that an approved review
// resets the Redis counter.
func TestCopilotFix_Approved_ResetsCounter(t *testing.T) {
	srv, _ := newGHMock(t)
	rdb := newMockRedis()
	fix := newCopilotFixLoopForTest("tok", rdb, srv.URL)

	ctx := context.Background()
	key := fix.attemptKey("owner/repo", 99)

	// Simulate a previous failed attempt
	_ = fix.HandleReview(ctx, "owner/repo", 99, "changes_requested")
	if rdb.get(key) != 1 {
		t.Fatal("counter should be 1 before reset")
	}

	// Approve should reset counter
	if err := fix.HandleReview(ctx, "owner/repo", 99, "approved"); err != nil {
		t.Fatalf("approved: %v", err)
	}
	if rdb.get(key) != 0 {
		t.Errorf("counter should be 0 after approval, got %d", rdb.get(key))
	}
}

// TestCopilotFix_Commented_DoesNothing verifies that a "commented" review
// state (not changes_requested) takes no action.
func TestCopilotFix_Commented_DoesNothing(t *testing.T) {
	srv, comments := newGHMock(t)
	rdb := newMockRedis()
	fix := newCopilotFixLoopForTest("tok", rdb, srv.URL)

	ctx := context.Background()
	if err := fix.HandleReview(ctx, "owner/repo", 55, "commented"); err != nil {
		t.Fatalf("HandleReview(commented): %v", err)
	}

	if len(*comments) != 0 {
		t.Errorf("expected 0 comments for 'commented' state, got %d", len(*comments))
	}
	key := fix.attemptKey("owner/repo", 55)
	if rdb.get(key) != 0 {
		t.Errorf("expected counter=0 for 'commented' state, got %d", rdb.get(key))
	}
}

// TestCopilotFix_ResetAttempts_ClearsKey verifies that ResetAttempts deletes
// the Redis key directly.
func TestCopilotFix_ResetAttempts_ClearsKey(t *testing.T) {
	rdb := newMockRedis()
	fix := &CopilotFixLoop{rdb: rdb, maxAttempts: defaultMaxAttempts}

	ctx := context.Background()
	key := fix.attemptKey("org/repo", 3)

	// Manually seed the counter
	rdb.Incr(ctx, key)
	rdb.Incr(ctx, key)
	if rdb.get(key) != 2 {
		t.Fatal("pre-condition: counter should be 2")
	}

	if err := fix.ResetAttempts(ctx, "org/repo", 3); err != nil {
		t.Fatalf("ResetAttempts: %v", err)
	}
	if rdb.get(key) != 0 {
		t.Errorf("expected counter=0 after reset, got %d", rdb.get(key))
	}
}
