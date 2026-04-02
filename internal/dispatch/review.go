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
)

// ReviewResult is the outcome of reviewing a PR.
type ReviewResult struct {
	Decision   string  `json:"decision"`   // "approve", "request_changes"
	Summary    string  `json:"summary"`    // one-line summary
	Comments   string  `json:"comments"`   // detailed review feedback
	Confidence float64 `json:"confidence"`
	CostCents  int     `json:"cost_cents"`
	Model      string  `json:"model"`
	Merged     bool    `json:"merged"`
}

// ReviewHandler reviews PRs via Claude API and approves/merges or requests changes.
// Runs on the Linux box — no secrets needed in GitHub Actions.
type ReviewHandler struct {
	ghToken string // GitHub PAT for review/merge
	apiKey  string // Anthropic API key
	model   string // default: claude-haiku-4-5-20251001
}

// NewReviewHandler creates a review handler. Reads tokens from env if empty.
func NewReviewHandler(ghToken, apiKey, model string) *ReviewHandler {
	if ghToken == "" {
		ghToken = os.Getenv("GITHUB_TOKEN")
	}
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	return &ReviewHandler{
		ghToken: ghToken,
		apiKey:  apiKey,
		model:   model,
	}
}

// HandlePR reviews a PR: fetch diff → Claude review → approve/merge or request changes.
func (rh *ReviewHandler) HandlePR(ctx context.Context, repo string, prNumber int) (*ReviewResult, error) {
	// Fetch PR metadata
	prMeta, err := rh.fetchPR(ctx, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch PR: %w", err)
	}

	// Fetch diff
	diff, err := rh.fetchDiff(ctx, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch diff: %w", err)
	}

	// Truncate large diffs to avoid blowing the context window
	if len(diff) > 30000 {
		diff = diff[:30000] + "\n\n... (diff truncated at 30KB)"
	}

	// Review via Claude API
	result, err := rh.review(ctx, repo, prMeta, diff)
	if err != nil {
		return nil, fmt.Errorf("review: %w", err)
	}

	// Post the review
	if result.Decision == "approve" {
		if err := rh.postReview(ctx, repo, prNumber, "APPROVE", result.Comments); err != nil {
			return result, fmt.Errorf("post approve: %w", err)
		}
		// Merge
		if err := rh.mergePR(ctx, repo, prNumber); err != nil {
			result.Merged = false
			return result, fmt.Errorf("merge: %w", err)
		}
		result.Merged = true
	} else {
		if err := rh.postReview(ctx, repo, prNumber, "REQUEST_CHANGES", result.Comments); err != nil {
			return result, fmt.Errorf("post request_changes: %w", err)
		}
	}

	return result, nil
}

type prMetadata struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	Author    string `json:"author"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Files     int    `json:"changed_files"`
}

func (rh *ReviewHandler) fetchPR(ctx context.Context, repo string, prNumber int) (*prMetadata, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+rh.ghToken)
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

func (rh *ReviewHandler) fetchDiff(ctx context.Context, repo string, prNumber int) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+rh.ghToken)
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

func (rh *ReviewHandler) review(ctx context.Context, repo string, pr *prMetadata, diff string) (*ReviewResult, error) {
	prompt := fmt.Sprintf(`You are a senior code reviewer for the %s repository. Review this pull request.

## PR Details
**Title:** %s
**Author:** %s
**Size:** +%d -%d lines, %d files

**Description:**
%s

## Diff
%s

## Instructions

Review the code for:
1. Correctness — does it do what the PR title/description says?
2. Bugs — any logic errors, off-by-one, null handling issues?
3. Security — any injection, XSS, secrets exposure?
4. Style — does it follow the repo's conventions?

If the change is straightforward, correct, and safe: approve it.
If there are issues: request changes with specific feedback.

Small, well-scoped PRs from automated agents (Copilot) should be approved if they're functionally correct, even if the code style isn't perfect.

Respond with ONLY a JSON object:
{"decision": "approve", "summary": "one sentence", "comments": "detailed feedback or 'LGTM'", "confidence": 0.9}

decision must be "approve" or "request_changes".`,
		repo, pr.Title, pr.Author, pr.Additions, pr.Deletions, pr.Files, pr.Body, diff)

	reqBody := map[string]interface{}{
		"model":      rh.model,
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("x-api-key", rh.apiKey)
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

	var reviewResp struct {
		Decision   string  `json:"decision"`
		Summary    string  `json:"summary"`
		Comments   string  `json:"comments"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(rawText), &reviewResp); err != nil {
		return nil, fmt.Errorf("parse review response: %w (raw: %s)", err, rawText)
	}

	// Validate decision
	switch reviewResp.Decision {
	case "approve", "request_changes":
		// valid
	default:
		reviewResp.Decision = "request_changes"
		reviewResp.Comments = "Review returned invalid decision, defaulting to request_changes for safety"
		reviewResp.Confidence = 0.0
	}

	// Estimate cost (Sonnet: $3/MTok input, $15/MTok output)
	costCents := (apiResp.Usage.InputTokens*300 + apiResp.Usage.OutputTokens*1500) / 1_000_000

	return &ReviewResult{
		Decision:   reviewResp.Decision,
		Summary:    reviewResp.Summary,
		Comments:   reviewResp.Comments,
		Confidence: reviewResp.Confidence,
		CostCents:  costCents,
		Model:      rh.model,
	}, nil
}

func (rh *ReviewHandler) postReview(ctx context.Context, repo string, prNumber int, event, body string) error {
	comment := fmt.Sprintf("🤖 **Octi Pulpo Review — %s**\n\n%s\n\n_Powered by Octi Pulpo pipeline_", event, body)

	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d/reviews", repo, prNumber)
	reqBody, _ := json.Marshal(map[string]string{
		"body":  comment,
		"event": event,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+rh.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("review API returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (rh *ReviewHandler) mergePR(ctx context.Context, repo string, prNumber int) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d/merge", repo, prNumber)
	reqBody, _ := json.Marshal(map[string]string{
		"merge_method": "squash",
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+rh.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("merge API returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
