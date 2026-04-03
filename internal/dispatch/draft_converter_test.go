package dispatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestShouldConvert verifies the promotion-eligibility logic in isolation.
func TestShouldConvert(t *testing.T) {
	tests := []struct {
		name   string
		author string
		title  string
		draft  bool
		action string
		want   bool
	}{
		{
			name:   "copilot draft with review_requested and clean title",
			author: "copilot-swe-agent[bot]",
			title:  "feat: add auto-retry logic",
			draft:  true,
			action: "review_requested",
			want:   true,
		},
		{
			name:   "non-draft PR is not promoted",
			author: "copilot-swe-agent[bot]",
			title:  "feat: add auto-retry logic",
			draft:  false,
			action: "review_requested",
			want:   false,
		},
		{
			name:   "non-copilot author is not promoted",
			author: "jpleva91",
			title:  "feat: add auto-retry logic",
			draft:  true,
			action: "review_requested",
			want:   false,
		},
		{
			name:   "WIP in title blocks promotion",
			author: "copilot-swe-agent[bot]",
			title:  "WIP: half-baked feature",
			draft:  true,
			action: "review_requested",
			want:   false,
		},
		{
			name:   "lowercase wip in title blocks promotion",
			author: "copilot-swe-agent[bot]",
			title:  "wip: half-baked feature",
			draft:  true,
			action: "review_requested",
			want:   false,
		},
		{
			name:   "bracketed [WIP] in title blocks promotion",
			author: "copilot-swe-agent[bot]",
			title:  "[WIP] half-baked feature",
			draft:  true,
			action: "review_requested",
			want:   false,
		},
		{
			name:   "wrong action is ignored",
			author: "copilot-swe-agent[bot]",
			title:  "feat: add auto-retry logic",
			draft:  true,
			action: "opened",
			want:   false,
		},
		{
			name:   "synchronize action is ignored",
			author: "copilot-swe-agent[bot]",
			title:  "feat: add auto-retry logic",
			draft:  true,
			action: "synchronize",
			want:   false,
		},
		{
			name:   "copilot-preview author matches",
			author: "copilot-preview[bot]",
			title:  "fix: resolve nil deref",
			draft:  true,
			action: "review_requested",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldConvert(tt.author, tt.title, tt.draft, tt.action)
			if got != tt.want {
				t.Errorf("ShouldConvert(%q, %q, draft=%v, action=%q) = %v, want %v",
					tt.author, tt.title, tt.draft, tt.action, got, tt.want)
			}
		})
	}
}

// TestContainsWIP checks the WIP detection helper.
func TestContainsWIP(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"WIP: something", true},
		{"wip: something", true},
		{"[WIP] something", true},
		{"[wip] something", true},
		{"feat: add something", false},
		{"fix: wipeable bug", true}, // contains "wip" substring
		{"", false},
	}
	for _, c := range cases {
		got := containsWIP(c.title)
		if got != c.want {
			t.Errorf("containsWIP(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}

// newTestDraftConverter creates a DraftConverter that talks to the given test server URL.
// It uses a dedicated http.Client so it never mutates http.DefaultTransport.
func newTestDraftConverter(serverURL, token string) *DraftConverter {
	dc := NewDraftConverter(token)
	dc.baseURL = serverURL
	dc.httpClient = &http.Client{}
	return dc
}

// TestConvertToReady verifies the GitHub API call for converting a draft PR.
func TestConvertToReady(t *testing.T) {
	var capturedMethod string
	var capturedPath string
	var capturedBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"number": 42,
			"draft":  false,
		})
	}))
	defer srv.Close()

	dc := newTestDraftConverter(srv.URL, "test-token")

	result, err := dc.ConvertToReady(context.Background(), "AgentGuardHQ/octi-pulpo", 42)
	if err != nil {
		t.Fatalf("ConvertToReady returned error: %v", err)
	}
	if !result.Converted {
		t.Error("expected Converted=true")
	}
	if result.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", result.PRNumber)
	}
	if capturedMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", capturedMethod)
	}
	if capturedPath != "/repos/AgentGuardHQ/octi-pulpo/pulls/42" {
		t.Errorf("path = %s, want /repos/AgentGuardHQ/octi-pulpo/pulls/42", capturedPath)
	}
	draftVal, ok := capturedBody["draft"].(bool)
	if !ok || draftVal != false {
		t.Errorf("body draft = %v, want false", capturedBody["draft"])
	}
}

// TestConvertToReadyAPIError verifies error handling when GitHub returns non-2xx.
func TestConvertToReadyAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Draft PRs cannot be converted"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	dc := newTestDraftConverter(srv.URL, "test-token")

	_, err := dc.ConvertToReady(context.Background(), "AgentGuardHQ/octi-pulpo", 99)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestSkipResult verifies the skip helper builds the right struct.
func TestSkipResult(t *testing.T) {
	r := SkipResult("AgentGuardHQ/octi-pulpo", 7, "not a copilot PR")
	if !r.Skipped {
		t.Error("expected Skipped=true")
	}
	if r.Converted {
		t.Error("expected Converted=false")
	}
	if r.Reason != "not a copilot PR" {
		t.Errorf("reason = %q", r.Reason)
	}
}
