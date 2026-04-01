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

// TriageResult is the outcome of classifying an issue.
type TriageResult struct {
	Tier       string  `json:"tier"`       // "tier:c", "tier:b-scope", "tier:a-groom"
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
	CostCents  int     `json:"cost_cents"`
	Model      string  `json:"model"`
}

// TriageHandler classifies GitHub issues via Claude API and labels them.
// It runs on the Linux box — no secrets needed in GitHub.
type TriageHandler struct {
	ghToken    string // GitHub PAT for labeling/commenting
	apiKey     string // Anthropic API key
	model      string // default: claude-haiku-4-5-20251001
	budgetName string // budget agent name for cost tracking
}

// NewTriageHandler creates a triage handler. Reads tokens from env if empty.
func NewTriageHandler(ghToken, apiKey, model string) *TriageHandler {
	if ghToken == "" {
		ghToken = os.Getenv("GITHUB_TOKEN")
	}
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	return &TriageHandler{
		ghToken:    ghToken,
		apiKey:     apiKey,
		model:      model,
		budgetName: "triage-agent",
	}
}

// HandleIssue triages a newly opened issue: classify → label → comment.
func (t *TriageHandler) HandleIssue(ctx context.Context, repo string, issueNumber int, title, body string, labels []string) (*TriageResult, error) {
	// Skip if already triaged
	for _, l := range labels {
		if strings.HasPrefix(l, "tier:") {
			return &TriageResult{Tier: l, Reason: "already triaged"}, nil
		}
	}

	// Classify via Claude API
	result, err := t.classify(ctx, repo, title, body, labels)
	if err != nil {
		// On API failure, default to tier:b-scope (safe fallback)
		result = &TriageResult{
			Tier:       "tier:b-scope",
			Reason:     fmt.Sprintf("triage error (defaulting to safe): %v", err),
			Confidence: 0.0,
		}
	}

	// Label the issue
	if err := t.addLabel(ctx, repo, issueNumber, result.Tier); err != nil {
		return result, fmt.Errorf("add label: %w", err)
	}

	// Remove triage:needed if present
	_ = t.removeLabel(ctx, repo, issueNumber, "triage:needed")

	// Post triage comment
	if err := t.postComment(ctx, repo, issueNumber, result); err != nil {
		return result, fmt.Errorf("post comment: %w", err)
	}

	return result, nil
}

func (t *TriageHandler) classify(ctx context.Context, repo, title, body string, labels []string) (*TriageResult, error) {
	prompt := fmt.Sprintf(`You are a triage agent for a GitHub repository. Classify this issue into exactly one tier.

## Tiers

- **tier:c** — Well-scoped, implementable by Copilot coding agent. Clear what to do, single repo, has enough detail.
- **tier:b-scope** — Needs planning/scoping before implementation. Vague requirements, missing acceptance criteria, architectural decisions needed, or multi-step work that needs decomposition.
- **tier:a-groom** — Needs human architect attention. Security implications, breaking changes, cross-repo impact, budget/cost decisions, or too ambiguous for AI to scope.

## Issue

**Repository:** %s
**Title:** %s
**Body:**
%s
**Existing Labels:** %s

## Instructions

Respond with ONLY a JSON object:
{"tier": "tier:c", "reason": "one sentence explanation", "confidence": 0.85}

If unsure between tier:c and tier:b-scope, choose tier:b-scope. If unsure between tier:b-scope and tier:a-groom, choose tier:b-scope.`,
		repo, title, body, strings.Join(labels, ", "))

	reqBody := map[string]interface{}{
		"model":      t.model,
		"max_tokens": 256,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("x-api-key", t.apiKey)
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

	// Parse Anthropic response
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

	// Parse the tier JSON from Claude's response
	var tierResp struct {
		Tier       string  `json:"tier"`
		Reason     string  `json:"reason"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(apiResp.Content[0].Text), &tierResp); err != nil {
		return nil, fmt.Errorf("parse tier response: %w (raw: %s)", err, apiResp.Content[0].Text)
	}

	// Validate tier
	switch tierResp.Tier {
	case "tier:c", "tier:b-scope", "tier:a-groom":
		// valid
	default:
		tierResp.Tier = "tier:b-scope"
		tierResp.Reason = "invalid tier returned, defaulting to safe option"
		tierResp.Confidence = 0.5
	}

	// Estimate cost (Haiku: $0.80/MTok input, $4/MTok output)
	costCents := (apiResp.Usage.InputTokens*80 + apiResp.Usage.OutputTokens*400) / 1_000_000

	return &TriageResult{
		Tier:       tierResp.Tier,
		Reason:     tierResp.Reason,
		Confidence: tierResp.Confidence,
		CostCents:  costCents,
		Model:      t.model,
	}, nil
}

func (t *TriageHandler) addLabel(ctx context.Context, repo string, issueNumber int, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels", repo, issueNumber)
	body, _ := json.Marshal(map[string][]string{"labels": {label}})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *TriageHandler) removeLabel(ctx context.Context, repo string, issueNumber int, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels/%s", repo, issueNumber, label)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *TriageHandler) postComment(ctx context.Context, repo string, issueNumber int, result *TriageResult) error {
	emoji := map[string]string{
		"tier:c":       "🤖",
		"tier:b-scope": "🧠",
		"tier:a-groom": "👤",
	}
	desc := map[string]string{
		"tier:c":       "**Tier C — Copilot Implementation.** Issue is well-scoped and ready for automated coding.",
		"tier:b-scope": "**Tier B — Needs Planning.** Issue requires scoping and decomposition before implementation.",
		"tier:a-groom": "**Tier A — Human Grooming Required.** Issue needs architect attention before proceeding.",
	}

	comment := fmt.Sprintf(`%s **Triage complete**

%s

**Reason:** %s
**Confidence:** %.2f

_Powered by Octi Pulpo pipeline_`,
		emoji[result.Tier], desc[result.Tier], result.Reason, result.Confidence)

	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repo, issueNumber)
	body, _ := json.Marshal(map[string]string{"body": comment})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
