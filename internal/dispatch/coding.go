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
	Implemented bool   `json:"implemented"` // true if code was successfully implemented
	Summary     string `json:"summary"`     // one-line summary of what was done
	Changes     string `json:"changes"`     // description of changes made
	CostCents   int    `json:"cost_cents"`
	Model       string `json:"model"`
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

	// TODO: Actually apply the fixes (create commit, push, etc.)
	// For now, we just return the analysis result
	// In Phase 3, we would implement the actual code application

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

	return &CodingResult{
		Implemented: fixResp.Implemented,
		Summary:     fixResp.Summary,
		Changes:     fixResp.Changes,
		CostCents:   costCents,
		Model:       c.model,
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