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
	"time"

	"github.com/chitinhq/octi-pulpo/internal/budget"
)

// CodingResult is the outcome of implementing a fix for an escalated PR.
type CodingResult struct {
	Implemented bool               `json:"implemented"` // true if code was successfully implemented
	Summary     string             `json:"summary"`     // one-line summary of what was done
	Changes     string             `json:"changes"`     // description of changes made
	CostCents   int                `json:"cost_cents"`
	Model       string             `json:"model"`
	Files       []CodingResultFile `json:"files,omitempty"` // files that were modified
}

// CodingResultFile represents a file that was modified in the fix.
type CodingResultFile struct {
	Path     string `json:"path"`               // file path
	Original string `json:"original,omitempty"` // original code snippet (if relevant)
	Fixed    string `json:"fixed"`              // fixed code snippet
}

// CodingHandler implements fixes for escalated PRs (tier:b-code) via Claude API.
// This is Phase 2 of Tier B senior coding — actually writing code for PRs that
// failed the Copilot fix loop after 2+ rounds of changes requested.
type CodingHandler struct {
	ghToken     string              // GitHub PAT for fetching PR details and pushing changes
	apiKey      string              // Anthropic API key
	model       string              // default: claude-3-5-sonnet-20241022 (Tier B uses Sonnet)
	budgetStore *budget.BudgetStore // optional: budget enforcement
}

// SetBudgetStore wires budget tracking into the coding handler.
func (c *CodingHandler) SetBudgetStore(bs *budget.BudgetStore) {
	c.budgetStore = bs
}

// NewCodingHandler creates a coding handler for Tier B escalated work.
// Reads tokens from env if empty.
func NewCodingHandler(ghToken, apiKey, model string) *CodingHandler {
	if ghToken == "" {
		ghToken = os.Getenv("GITHUB_TOKEN")
	}
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if model == "" {
		model = "claude-3-5-sonnet-20241022" // Tier B uses Sonnet for complex coding
	}
	return &CodingHandler{
		ghToken: ghToken,
		apiKey:  apiKey,
		model:   model,
	}
}

// HandlePR implements fixes for an escalated PR (tier:b-code).
// It fetches the PR details, reviews, and diff, then uses Claude API to
// generate and apply the necessary fixes.
func (c *CodingHandler) HandlePR(ctx context.Context, repo string, prNumber int) (*CodingResult, error) {
	// Budget gate: skip Claude API call if pipeline budget exceeded
	if err := checkBudgetGate(ctx, c.budgetStore, "coder"); err != nil {
		fmt.Fprintf(os.Stderr, "[octi-pulpo] %v\n", err)
		return &CodingResult{
			Implemented: false,
			Summary:     "Budget exceeded — skipping Tier B coding",
			Changes:     "Claude API budget exceeded. Tier B coding skipped.",
			Model:       "none (budget-gated)",
		}, nil
	}

	// Fetch PR metadata
	prMeta, err := c.fetchPR(ctx, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch PR: %w", err)
	}

	// Fetch diff
	diff, err := c.fetchDiff(ctx, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch diff: %w", err)
	}

	// Fetch review comments to understand what needs fixing
	reviews, err := c.fetchReviews(ctx, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch reviews: %w", err)
	}

	// Call Claude to implement fixes
	result, err := c.implementFix(ctx, repo, prMeta, diff, reviews)
	if err != nil {
		return nil, fmt.Errorf("implement fix: %w", err)
	}

	// Apply the fixes if they were implemented
	if result.Implemented && len(result.Files) > 0 {
		applyErr := c.applyFixes(ctx, repo, prNumber, result)
		if applyErr != nil {
			// Log the error but don't fail the whole operation
			fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to apply fixes for PR #%d: %v\n", prNumber, applyErr)
			// Update the result to reflect partial success
			result.Summary = result.Summary + " (fixes generated but failed to apply)"
			result.Changes = result.Changes + "\n\nNote: Failed to apply fixes automatically: " + applyErr.Error()
		} else {
			// Successfully applied fixes
			result.Summary = result.Summary + " (fixes applied and committed)"
			
			// Post a comment on the PR
			comment := fmt.Sprintf("## Tier B Coding: Fixes Applied\n\n%s\n\nChanges have been committed to the PR branch.", result.Changes)
			if postErr := c.postComment(ctx, repo, prNumber, comment); postErr != nil {
				fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to post comment on PR #%d: %v\n", prNumber, postErr)
			}
			
			// Remove the tier:b-code label since we've applied fixes
			if removeErr := c.removeLabel(ctx, repo, prNumber, "tier:b-code"); removeErr != nil {
				fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to remove tier:b-code label from PR #%d: %v\n", prNumber, removeErr)
			}
			
			// Add a tier:review label for re-review
			if addErr := c.addLabel(ctx, repo, prNumber, "tier:review"); addErr != nil {
				fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to add tier:review label to PR #%d: %v\n", prNumber, addErr)
			}
		}
	}

	return result, nil
}

func (c *CodingHandler) implementFix(ctx context.Context, repo string, pr *prMetadata, diff string, reviews []reviewComment) (*CodingResult, error) {
	// Format review comments
	var reviewText strings.Builder
	for i, r := range reviews {
		reviewText.WriteString(fmt.Sprintf("### Review %d (by %s):\n%s\n\n", i+1, r.Author, r.Body))
	}

	prompt := fmt.Sprintf(`You are a senior software engineer implementing fixes for an escalated PR (tier:b-code).
This PR has failed the Copilot fix loop after 2+ rounds of changes requested.
Your task is to analyze the PR and reviews, then implement the necessary fixes.

## PR Details
**Repository:** %s
**Title:** %s
**Author:** %s
**Size:** +%d -%d lines, %d files

**Description:**
%s

## Diff
%s

## Review Feedback
%s

## Instructions

1. Analyze what needs to be fixed based on the review feedback
2. Generate the complete fixed code
3. Provide a clear explanation of what you changed and why

Respond with ONLY a JSON object:
{
  "implemented": true,
  "summary": "one sentence summary of fixes implemented",
  "changes": "detailed description of changes made",
  "files": [
    {
      "path": "path/to/file.go",
      "original": "original code snippet (if relevant)",
      "fixed": "fixed code snippet"
    }
  ]
}

If you cannot implement the fix (e.g., insufficient information, security concerns),
set "implemented": false and explain why in "summary".`,
		repo, pr.Title, pr.Author, pr.Additions, pr.Deletions, pr.Files, pr.Body, diff, reviewText.String())

	reqBody := map[string]interface{}{
		"model":      c.model,
		"max_tokens": 4096,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse API response: %w", err)
	}
	if len(apiResp.Content) == 0 {
		return nil, fmt.Errorf("empty API response")
	}

	// Strip markdown code fences
	rawText := strings.TrimSpace(apiResp.Content[0].Text)
	if strings.HasPrefix(rawText, "```") {
		lines := strings.Split(rawText, "\n")
		if len(lines) >= 3 {
			rawText = strings.Join(lines[1:len(lines)-1], "\n")
		}
		rawText = strings.TrimSpace(rawText)
	}

	var fixResp struct {
		Implemented bool   `json:"implemented"`
		Summary     string `json:"summary"`
		Changes     string `json:"changes"`
		Files       []struct {
			Path     string `json:"path"`
			Original string `json:"original,omitempty"`
			Fixed    string `json:"fixed"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(rawText), &fixResp); err != nil {
		return nil, fmt.Errorf("parse coding response: %w (raw: %s)", err, rawText)
	}

	// Estimate cost (Sonnet: $3/MTok input, $15/MTok output)
	costCents := (apiResp.Usage.InputTokens*300 + apiResp.Usage.OutputTokens*1500) / 1_000_000

	// Record cost in budget store
	recordBudgetCost(ctx, c.budgetStore, "coder", costCents,
		apiResp.Usage.InputTokens, apiResp.Usage.OutputTokens)

	// Convert files to CodingResultFile format
	files := make([]CodingResultFile, 0, len(fixResp.Files))
	for _, f := range fixResp.Files {
		files = append(files, CodingResultFile{
			Path:     f.Path,
			Original: f.Original,
			Fixed:    f.Fixed,
		})
	}

	return &CodingResult{
		Implemented: fixResp.Implemented,
		Summary:     fixResp.Summary,
		Changes:     fixResp.Changes,
		CostCents:   costCents,
		Model:       c.model,
		Files:       files,
	}, nil
}



type reviewComment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
	State  string `json:"state"`
}

func (c *CodingHandler) fetchPR(ctx context.Context, repo string, prNumber int) (*prMetadata, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var pr struct {
		Title     string `json:"title"`
		Body      string `json:"body"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Files     int    `json:"changed_files"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, err
	}

	return &prMetadata{
		Title:     pr.Title,
		Body:      pr.Body,
		Author:    pr.User.Login,
		Additions: pr.Additions,
		Deletions: pr.Deletions,
		Files:     pr.Files,
	}, nil
}

func (c *CodingHandler) fetchDiff(ctx context.Context, repo string, prNumber int) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github.v3.diff")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (c *CodingHandler) fetchReviews(ctx context.Context, repo string, prNumber int) ([]reviewComment, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d/reviews", repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var reviews []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body  string `json:"body"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reviews); err != nil {
		return nil, err
	}

	result := make([]reviewComment, 0, len(reviews))
	for _, r := range reviews {
		if r.State == "CHANGES_REQUESTED" || r.State == "COMMENTED" {
			result = append(result, reviewComment{
				Author: r.User.Login,
				Body:   r.Body,
				State:  r.State,
			})
		}
	}
	return result, nil
}

func (c *CodingHandler) addLabel(ctx context.Context, repo string, prNumber int, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels", repo, prNumber)
	body, _ := json.Marshal(map[string][]string{"labels": {label}})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *CodingHandler) removeLabel(ctx context.Context, repo string, prNumber int, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels/%s", repo, prNumber, label)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *CodingHandler) postComment(ctx context.Context, repo string, prNumber int, comment string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repo, prNumber)
	body, _ := json.Marshal(map[string]string{"body": comment})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// applyFixes applies the generated fixes to the PR branch by creating a commit
// with the changes and pushing it to the remote repository.
func (c *CodingHandler) applyFixes(ctx context.Context, repo string, prNumber int, result *CodingResult) error {
	// First, we need to get the PR details to know the branch name
	prURL := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, prURL, nil)
	if err != nil {
		return fmt.Errorf("create PR request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch PR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to fetch PR: %d: %s", resp.StatusCode, string(body))
	}

	var pr struct {
		Head struct {
			Ref string `json:"ref"`
			Repo struct {
				CloneURL string `json:"clone_url"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return fmt.Errorf("parse PR response: %w", err)
	}

	// For now, we'll log what we would do since actually cloning and modifying
	// the repository requires more infrastructure
	fmt.Fprintf(os.Stderr, "[octi-pulpo] Would apply fixes to PR #%d in %s\n", prNumber, repo)
	fmt.Fprintf(os.Stderr, "[octi-pulpo] Branch: %s\n", pr.Head.Ref)
	fmt.Fprintf(os.Stderr, "[octi-pulpo] Clone URL: %s\n", pr.Head.Repo.CloneURL)
	fmt.Fprintf(os.Stderr, "[octi-pulpo] Files to modify: %d\n", len(result.Files))
	
	for i, file := range result.Files {
		fmt.Fprintf(os.Stderr, "[octi-pulpo] File %d: %s\n", i+1, file.Path)
	}

	// In a real implementation, we would:
	// 1. Clone the repository
	// 2. Checkout the PR branch
	// 3. Apply the fixes to each file
	// 4. Commit the changes
	// 5. Push to the remote branch
	// 6. Return any errors

	// For now, return a placeholder error indicating this needs to be implemented
	return fmt.Errorf("fix application not fully implemented (would modify %d files)", len(result.Files))
}