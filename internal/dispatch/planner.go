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

// PlannerResult is the outcome of scoping a vague issue.
type PlannerResult struct {
	AcceptanceCriteria string     `json:"acceptance_criteria"`
	SubIssues          []SubIssue `json:"sub_issues,omitempty"`
	Escalate           bool       `json:"escalate"`
	Reason             string     `json:"reason"`
	CostCents          int        `json:"cost_cents"`
	Model              string     `json:"model"`
}

// SubIssue is a well-scoped child issue created from a vague parent.
type SubIssue struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// PlannerHandler scopes vague issues via Claude API — writes acceptance criteria,
// optionally splits into sub-issues, then relabels tier:c for Copilot.
// Runs on the Linux box — no secrets needed in GitHub.
type PlannerHandler struct {
	ghToken string
	apiKey  string
	model   string
}

// NewPlannerHandler creates a planner handler. Reads tokens from env if empty.
func NewPlannerHandler(ghToken, apiKey, model string) *PlannerHandler {
	if ghToken == "" {
		ghToken = os.Getenv("GITHUB_TOKEN")
	}
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	return &PlannerHandler{
		ghToken: ghToken,
		apiKey:  apiKey,
		model:   model,
	}
}

// HandleIssue scopes a vague issue: analyze → write criteria → optionally split → relabel.
func (p *PlannerHandler) HandleIssue(ctx context.Context, repo string, issueNumber int, title, body string) (*PlannerResult, error) {
	// Fetch open issues for context (what's already being worked on)
	openIssues, _ := p.fetchOpenIssueTitles(ctx, repo)

	// Call Claude to scope
	result, err := p.scope(ctx, repo, title, body, openIssues)
	if err != nil {
		return nil, fmt.Errorf("scope: %w", err)
	}

	// If Claude says escalate, move to tier:a-groom
	if result.Escalate {
		_ = p.removeLabel(ctx, repo, issueNumber, "tier:b-scope")
		_ = p.addLabel(ctx, repo, issueNumber, "tier:a-groom")
		_ = p.postComment(ctx, repo, issueNumber, fmt.Sprintf(
			"👤 **Planner — Escalating to Tier A**\n\n%s\n\n_Powered by Octi Pulpo pipeline_",
			result.Reason))
		return result, nil
	}

	// Update the issue body with acceptance criteria
	_ = p.postComment(ctx, repo, issueNumber, fmt.Sprintf(
		"🧠 **Planner — Issue Scoped**\n\n## Acceptance Criteria\n\n%s\n\n_Powered by Octi Pulpo pipeline_",
		result.AcceptanceCriteria))

	// Create sub-issues if needed
	for _, sub := range result.SubIssues {
		subBody := fmt.Sprintf("%s\n\n---\n_Parent: #%d_\n_Created by Octi Pulpo planner_", sub.Body, issueNumber)
		subNumber, err := p.createIssue(ctx, repo, sub.Title, subBody)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[octi-pulpo] planner: failed to create sub-issue: %v\n", err)
			continue
		}
		// Label sub-issues as tier:c — ready for Copilot
		_ = p.addLabel(ctx, repo, subNumber, "tier:c")
	}

	if len(result.SubIssues) > 0 {
		// Parent issue is now a tracking issue — close it or keep it open
		_ = p.postComment(ctx, repo, issueNumber, fmt.Sprintf(
			"📋 **Split into %d sub-issues** — each labeled tier:c for Copilot.\n\n_Powered by Octi Pulpo pipeline_",
			len(result.SubIssues)))
		_ = p.removeLabel(ctx, repo, issueNumber, "tier:b-scope")
	} else {
		// Single issue, now well-scoped — relabel tier:c
		_ = p.removeLabel(ctx, repo, issueNumber, "tier:b-scope")
		_ = p.addLabel(ctx, repo, issueNumber, "tier:c")
	}

	return result, nil
}

func (p *PlannerHandler) scope(ctx context.Context, repo, title, body string, openIssues []string) (*PlannerResult, error) {
	prompt := fmt.Sprintf(`You are a tech lead scoping a GitHub issue for implementation by Copilot (an AI coding agent).

## Issue
**Repository:** %s
**Title:** %s
**Body:**
%s

## Currently Open Issues (for context — avoid duplicates)
%s

## Instructions

Analyze this issue and do ONE of:

1. **Scope it** — if you can write clear acceptance criteria, do so. If the work is large, split into 2-4 well-scoped sub-issues.
2. **Escalate** — if you can't scope it (security, breaking changes, cross-repo, too ambiguous), set escalate=true.

Respond with ONLY a JSON object:
{
  "acceptance_criteria": "markdown bullet list of clear, testable criteria",
  "sub_issues": [{"title": "feat: ...", "body": "## Summary\n...\n\n## Acceptance Criteria\n..."}],
  "escalate": false,
  "reason": "one sentence explanation of your decision"
}

Rules:
- sub_issues should be empty if the issue is small enough for one PR
- Each sub-issue must be independently implementable and well-scoped
- Sub-issue bodies must include Summary and Acceptance Criteria sections
- If escalating, acceptance_criteria and sub_issues should be empty`,
		repo, title, body, strings.Join(openIssues, "\n"))

	reqBody := map[string]interface{}{
		"model":      p.model,
		"max_tokens": 2048,
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

	req.Header.Set("x-api-key", p.apiKey)
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

	var planResp struct {
		AcceptanceCriteria string     `json:"acceptance_criteria"`
		SubIssues          []SubIssue `json:"sub_issues"`
		Escalate           bool       `json:"escalate"`
		Reason             string     `json:"reason"`
	}
	if err := json.Unmarshal([]byte(rawText), &planResp); err != nil {
		return nil, fmt.Errorf("parse planner response: %w (raw: %s)", err, rawText)
	}

	costCents := (apiResp.Usage.InputTokens*80 + apiResp.Usage.OutputTokens*400) / 1_000_000

	return &PlannerResult{
		AcceptanceCriteria: planResp.AcceptanceCriteria,
		SubIssues:          planResp.SubIssues,
		Escalate:           planResp.Escalate,
		Reason:             planResp.Reason,
		CostCents:          costCents,
		Model:              p.model,
	}, nil
}

func (p *PlannerHandler) fetchOpenIssueTitles(ctx context.Context, repo string) ([]string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues?state=open&per_page=30", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var issues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, err
	}

	titles := make([]string, 0, len(issues))
	for _, issue := range issues {
		titles = append(titles, fmt.Sprintf("#%d: %s", issue.Number, issue.Title))
	}
	return titles, nil
}

func (p *PlannerHandler) createIssue(ctx context.Context, repo, title, body string) (int, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues", repo)
	reqBody, _ := json.Marshal(map[string]string{"title": title, "body": body})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var created struct {
		Number int `json:"number"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return 0, err
	}
	return created.Number, nil
}

func (p *PlannerHandler) addLabel(ctx context.Context, repo string, issueNumber int, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels", repo, issueNumber)
	body, _ := json.Marshal(map[string][]string{"labels": {label}})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (p *PlannerHandler) removeLabel(ctx context.Context, repo string, issueNumber int, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels/%s", repo, issueNumber, label)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (p *PlannerHandler) postComment(ctx context.Context, repo string, issueNumber int, comment string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repo, issueNumber)
	body, _ := json.Marshal(map[string]string{"body": comment})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
