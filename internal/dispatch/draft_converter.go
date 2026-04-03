package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// DraftConvertResult is the outcome of a draft-to-ready conversion attempt.
type DraftConvertResult struct {
	PRNumber  int    `json:"pr_number"`
	Repo      string `json:"repo"`
	Converted bool   `json:"converted"`
	Skipped   bool   `json:"skipped"`
	Reason    string `json:"reason"`
}

// DraftConverter converts Copilot draft PRs to ready-for-review when they
// have a reviewer assigned and no WIP marker in the title.
//
// Trigger condition (all must be true):
//   - PR author login contains "copilot"
//   - PR is currently a draft (draft == true)
//   - GitHub action is "review_requested" (Copilot signalled it wants review)
//   - PR title does not contain "WIP" or "wip" (case-insensitive)
type DraftConverter struct {
	ghToken    string       // GitHub PAT — reads GITHUB_TOKEN from env if empty
	baseURL    string       // GitHub API base URL (overridable for tests)
	httpClient *http.Client // HTTP client (overridable for tests)
}

// NewDraftConverter creates a DraftConverter. Reads GITHUB_TOKEN from env if ghToken is empty.
func NewDraftConverter(ghToken string) *DraftConverter {
	if ghToken == "" {
		ghToken = os.Getenv("GITHUB_TOKEN")
	}
	return &DraftConverter{
		ghToken:    ghToken,
		baseURL:    "https://api.github.com",
		httpClient: http.DefaultClient,
	}
}

// ShouldConvert returns true when the PR matches the draft-to-ready promotion criteria.
//
// It accepts the raw PR metadata so callers can pass values extracted from the
// GitHub webhook payload without making additional API calls.
func ShouldConvert(author, title string, isDraft bool, action string) bool {
	if action != "review_requested" {
		return false
	}
	if !isDraft {
		return false
	}
	if !strings.Contains(strings.ToLower(author), "copilot") {
		return false
	}
	if containsWIP(title) {
		return false
	}
	return true
}

// containsWIP returns true if the title contains a WIP marker.
// Recognised patterns: standalone "WIP" / "wip" word or bracketed "[WIP]" / "[wip]" prefix.
func containsWIP(title string) bool {
	lower := strings.ToLower(title)
	// Match "[wip]" anywhere in the title (bracketed form).
	if strings.Contains(lower, "[wip]") {
		return true
	}
	// Match "wip" as a standalone word or colon-prefixed marker (e.g. "wip: ..." or "wip ").
	for _, part := range strings.Fields(lower) {
		// Strip trailing punctuation common in prefixes like "wip:"
		word := strings.TrimRight(part, ":,")
		if word == "wip" {
			return true
		}
	}
	return false
}

// ConvertToReady promotes a draft PR to ready-for-review via the GitHub API.
// This is equivalent to running `gh pr ready <number>` from the CLI.
func (dc *DraftConverter) ConvertToReady(ctx context.Context, repo string, prNumber int) (*DraftConvertResult, error) {
	if dc.ghToken == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN not set: cannot convert PR #%d to ready-for-review", prNumber)
	}

	result := &DraftConvertResult{
		PRNumber: prNumber,
		Repo:     repo,
	}

	// GitHub GraphQL convertPullRequestToDraft inverse: use the REST "ready for review" endpoint.
	// PATCH /repos/{owner}/{repo}/pulls/{pull_number}  with {"draft": false}
	apiURL := fmt.Sprintf("%s/repos/%s/pulls/%d", dc.baseURL, repo, prNumber)
	body, err := json.Marshal(map[string]bool{"draft": false})
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+dc.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := dc.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(respBody))
	}

	result.Converted = true
	result.Reason = "review_requested + no WIP in title — promoted to ready"
	fmt.Fprintf(os.Stderr, "[octi-pulpo] draft→ready: PR #%d in %s converted\n", prNumber, repo)
	return result, nil
}

// SkipResult builds a DraftConvertResult for PRs that do not meet the promotion criteria.
func SkipResult(repo string, prNumber int, reason string) *DraftConvertResult {
	return &DraftConvertResult{
		PRNumber: prNumber,
		Repo:     repo,
		Skipped:  true,
		Reason:   reason,
	}
}
