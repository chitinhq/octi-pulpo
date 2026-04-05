package dispatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// newTestBranchUpdater creates a BranchUpdater that talks to the given test server URL.
func newTestBranchUpdater(serverURL, token string) *BranchUpdater {
	bu := NewBranchUpdater(token)
	bu.baseURL = serverURL
	bu.httpClient = &http.Client{}
	return bu
}

func TestHandlePush_UpdatesBehindPRs(t *testing.T) {
	var mu sync.Mutex
	var updateCalls []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// GET /repos/:owner/:repo/pulls?state=open&base=main...
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls") && r.URL.Query().Get("state") == "open" {
			json.NewEncoder(w).Encode([]prListItem{
				{Number: 10, State: "open", Base: struct {
					Ref string `json:"ref"`
				}{Ref: "main"}, Head: struct {
					Ref string `json:"ref"`
					SHA string `json:"sha"`
				}{Ref: "feat-a", SHA: "aaa111"}},
				{Number: 20, State: "open", Base: struct {
					Ref string `json:"ref"`
				}{Ref: "main"}, Head: struct {
					Ref string `json:"ref"`
					SHA string `json:"sha"`
				}{Ref: "feat-b", SHA: "bbb222"}},
			})
			return
		}

		// GET /repos/:owner/:repo/compare/main...{sha}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/") {
			behind := 0
			if strings.HasSuffix(r.URL.Path, "aaa111") {
				behind = 3 // PR #10 is behind
			}
			json.NewEncoder(w).Encode(compareResponse{BehindBy: behind})
			return
		}

		// PUT /repos/:owner/:repo/pulls/:number/update-branch
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/update-branch") {
			mu.Lock()
			// Extract PR number from path: .../pulls/10/update-branch
			parts := strings.Split(r.URL.Path, "/")
			for i, p := range parts {
				if p == "pulls" && i+1 < len(parts) {
					var n int
					_ = json.Unmarshal([]byte(parts[i+1]), &n) //nolint:errcheck
					updateCalls = append(updateCalls, n)
				}
			}
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"message": "Updating pull request branch."})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	bu := newTestBranchUpdater(srv.URL, "test-token")

	results, err := bu.HandlePush(context.Background(), "chitinhq/octi-pulpo", "main")
	if err != nil {
		t.Fatalf("HandlePush: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// PR #10 should be updated (behind by 3)
	if !results[0].Updated {
		t.Errorf("PR #10: expected Updated=true, got Updated=%v Reason=%s", results[0].Updated, results[0].Reason)
	}
	// PR #20 should be skipped (up to date)
	if !results[1].Skipped {
		t.Errorf("PR #20: expected Skipped=true, got Skipped=%v", results[1].Skipped)
	}
	if results[1].Reason != "already up to date" {
		t.Errorf("PR #20: reason = %q, want %q", results[1].Reason, "already up to date")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updateCalls) != 1 {
		t.Fatalf("expected 1 update-branch call, got %d", len(updateCalls))
	}
	if updateCalls[0] != 10 {
		t.Errorf("update-branch called for PR #%d, want #10", updateCalls[0])
	}
}

func TestHandlePush_NoPRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]prListItem{})
	}))
	defer srv.Close()

	bu := newTestBranchUpdater(srv.URL, "test-token")

	results, err := bu.HandlePush(context.Background(), "chitinhq/octi-pulpo", "main")
	if err != nil {
		t.Fatalf("HandlePush: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestHandlePush_NoTokenReturnsError(t *testing.T) {
	bu := NewBranchUpdater("")
	bu.ghToken = ""

	_, err := bu.HandlePush(context.Background(), "chitinhq/octi-pulpo", "main")
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
}

func TestHandlePush_APIErrorOnListPRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"internal error"}`))
	}))
	defer srv.Close()

	bu := newTestBranchUpdater(srv.URL, "test-token")

	_, err := bu.HandlePush(context.Background(), "chitinhq/octi-pulpo", "main")
	if err == nil {
		t.Fatal("expected error on API failure, got nil")
	}
}

func TestHandlePush_CompareErrorSkipsPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls") && r.URL.Query().Get("state") == "open" {
			json.NewEncoder(w).Encode([]prListItem{
				{Number: 5, State: "open", Base: struct {
					Ref string `json:"ref"`
				}{Ref: "main"}, Head: struct {
					Ref string `json:"ref"`
					SHA string `json:"sha"`
				}{Ref: "feat-c", SHA: "ccc333"}},
			})
			return
		}

		// Compare endpoint returns 500
		if strings.Contains(r.URL.Path, "/compare/") {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"message":"compare failed"}`))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	bu := newTestBranchUpdater(srv.URL, "test-token")

	results, err := bu.HandlePush(context.Background(), "chitinhq/octi-pulpo", "main")
	if err != nil {
		t.Fatalf("HandlePush: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Skipped {
		t.Error("expected Skipped=true when compare fails")
	}
	if !strings.Contains(results[0].Reason, "compare failed") {
		t.Errorf("reason = %q, want it to mention compare failure", results[0].Reason)
	}
}

func TestHandlePush_UpdateBranchErrorSkipsPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls") && r.URL.Query().Get("state") == "open" {
			json.NewEncoder(w).Encode([]prListItem{
				{Number: 7, State: "open", Base: struct {
					Ref string `json:"ref"`
				}{Ref: "main"}, Head: struct {
					Ref string `json:"ref"`
					SHA string `json:"sha"`
				}{Ref: "feat-d", SHA: "ddd444"}},
			})
			return
		}

		if strings.Contains(r.URL.Path, "/compare/") {
			json.NewEncoder(w).Encode(compareResponse{BehindBy: 2})
			return
		}

		// Update branch returns 422
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/update-branch") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"merge conflict"}`))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	bu := newTestBranchUpdater(srv.URL, "test-token")

	results, err := bu.HandlePush(context.Background(), "chitinhq/octi-pulpo", "main")
	if err != nil {
		t.Fatalf("HandlePush: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Updated {
		t.Error("should not be Updated when update-branch returns 422")
	}
	if !results[0].Skipped {
		t.Error("expected Skipped=true when update-branch fails")
	}
	if !strings.Contains(results[0].Reason, "update failed") {
		t.Errorf("reason = %q, want it to mention update failure", results[0].Reason)
	}
}
