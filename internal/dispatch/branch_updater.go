package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// BranchUpdateResult summarises a single PR branch update attempt.
type BranchUpdateResult struct {
	PRNumber int    `json:"pr_number"`
	Updated  bool   `json:"updated"`
	Skipped  bool   `json:"skipped"`
	Reason   string `json:"reason"`
}

// BranchUpdater auto-updates open PR branches that fall behind the default
// branch after a push. This keeps CI green and reduces merge conflicts.
type BranchUpdater struct {
	ghToken    string       // GitHub PAT — reads GITHUB_TOKEN from env if empty
	baseURL    string       // GitHub API base URL (overridable for tests)
	httpClient *http.Client // HTTP client (overridable for tests)
}

// NewBranchUpdater creates a BranchUpdater. Reads GITHUB_TOKEN from env if ghToken is empty.
func NewBranchUpdater(ghToken string) *BranchUpdater {
	if ghToken == "" {
		ghToken = os.Getenv("GITHUB_TOKEN")
	}
	return &BranchUpdater{
		ghToken:    ghToken,
		baseURL:    "https://api.github.com",
		httpClient: http.DefaultClient,
	}
}

// prListItem is the subset of the GitHub PR response we need.
type prListItem struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

// compareResponse is the subset of the GitHub compare API we need.
type compareResponse struct {
	BehindBy int `json:"behind_by"`
}

// HandlePush lists open PRs targeting the default branch and updates any
// that are behind. It is designed to be called from the webhook handler
// when a push to the default branch is detected.
func (bu *BranchUpdater) HandlePush(ctx context.Context, repo, defaultBranch string) ([]BranchUpdateResult, error) {
	if bu.ghToken == "" {
		return nil, fmt.Errorf("branch updater: no GitHub token configured")
	}

	prs, err := bu.listOpenPRs(ctx, repo, defaultBranch)
	if err != nil {
		return nil, fmt.Errorf("list open PRs: %w", err)
	}

	var results []BranchUpdateResult
	for _, pr := range prs {
		result := bu.updateIfBehind(ctx, repo, defaultBranch, pr)
		results = append(results, result)
	}
	return results, nil
}

// listOpenPRs fetches open PRs targeting the given base branch.
func (bu *BranchUpdater) listOpenPRs(ctx context.Context, repo, base string) ([]prListItem, error) {
	url := fmt.Sprintf("%s/repos/%s/pulls?state=open&base=%s&per_page=100", bu.baseURL, repo, base)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bu.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := bu.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list PRs: status %d: %s", resp.StatusCode, body)
	}

	var prs []prListItem
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return nil, fmt.Errorf("decode PRs: %w", err)
	}
	return prs, nil
}

// updateIfBehind checks whether a PR branch is behind the base and calls
// the GitHub update-branch API if so.
func (bu *BranchUpdater) updateIfBehind(ctx context.Context, repo, defaultBranch string, pr prListItem) BranchUpdateResult {
	behind, err := bu.isBehind(ctx, repo, defaultBranch, pr.Head.SHA)
	if err != nil {
		return BranchUpdateResult{PRNumber: pr.Number, Skipped: true, Reason: fmt.Sprintf("compare failed: %v", err)}
	}
	if !behind {
		return BranchUpdateResult{PRNumber: pr.Number, Skipped: true, Reason: "already up to date"}
	}

	if err := bu.updateBranch(ctx, repo, pr.Number); err != nil {
		return BranchUpdateResult{PRNumber: pr.Number, Skipped: true, Reason: fmt.Sprintf("update failed: %v", err)}
	}
	return BranchUpdateResult{PRNumber: pr.Number, Updated: true, Reason: "updated"}
}

// isBehind returns true if headSHA is behind the default branch.
func (bu *BranchUpdater) isBehind(ctx context.Context, repo, base, headSHA string) (bool, error) {
	url := fmt.Sprintf("%s/repos/%s/compare/%s...%s", bu.baseURL, repo, base, headSHA)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+bu.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := bu.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("compare: status %d: %s", resp.StatusCode, body)
	}

	var cmp compareResponse
	if err := json.NewDecoder(resp.Body).Decode(&cmp); err != nil {
		return false, fmt.Errorf("decode compare: %w", err)
	}
	return cmp.BehindBy > 0, nil
}

// updateBranch calls the GitHub "update a pull request branch" API.
func (bu *BranchUpdater) updateBranch(ctx context.Context, repo string, prNumber int) error {
	url := fmt.Sprintf("%s/repos/%s/pulls/%d/update-branch", bu.baseURL, repo, prNumber)
	body, _ := json.Marshal(map[string]string{"update_method": "merge"})

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bu.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := bu.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update branch: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}
