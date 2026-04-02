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

// CascadeAction is a single action that the cascade handler should execute.
type CascadeAction struct {
	Type        string `json:"type"`                  // "create", "close", "relabel"
	Repo        string `json:"repo"`                  // target repo full name
	Title       string `json:"title,omitempty"`        // for create
	Body        string `json:"body,omitempty"`         // for create
	IssueNumber int    `json:"issue_number,omitempty"` // for close/relabel
	Reason      string `json:"reason,omitempty"`       // explanation
	Labels      []string `json:"labels,omitempty"`     // for relabel
}

// CascadeResult is the outcome of a strategy cascade run.
type CascadeResult struct {
	Actions   []CascadeAction `json:"actions"`
	Created   int             `json:"created"`
	Closed    int             `json:"closed"`
	Relabeled int             `json:"relabeled"`
	Errors    []string        `json:"errors,omitempty"`
	CostCents int             `json:"cost_cents"`
	Model     string          `json:"model"`
}

// CascadeHandler diffs roadmap.md against open issues across repos and
// creates/closes/relabels issues to keep them in sync with the strategy.
// Triggered by push events to agentguard-workspace when roadmap.md changes.
type CascadeHandler struct {
	ghToken     string   // GitHub PAT for creating/closing issues
	apiKey      string   // Anthropic API key
	model       string   // default: claude-haiku-4-5-20251001
	targetRepos []string // repos to cascade to
}

// DefaultCascadeRepos is the list of repos the cascade handler manages.
var DefaultCascadeRepos = []string{
	"AgentGuardHQ/agentguard-cloud",
	"AgentGuardHQ/agentguard",
	"AgentGuardHQ/octi-pulpo",
	"AgentGuardHQ/shellforge",
	"AgentGuardHQ/agentguard-analytics",
}

// NewCascadeHandler creates a cascade handler. Reads tokens from env if empty.
func NewCascadeHandler(ghToken, apiKey, model string) *CascadeHandler {
	if ghToken == "" {
		ghToken = os.Getenv("GITHUB_TOKEN")
	}
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	return &CascadeHandler{
		ghToken:     ghToken,
		apiKey:      apiKey,
		model:       model,
		targetRepos: DefaultCascadeRepos,
	}
}

// HandlePush is called when roadmap.md is pushed to agentguard-workspace.
// It fetches the roadmap, fetches existing cascade:managed issues, diffs them
// via Claude, and executes the resulting actions.
func (ch *CascadeHandler) HandlePush(ctx context.Context) (*CascadeResult, error) {
	// 1. Fetch roadmap.md from agentguard-workspace
	roadmap, err := ch.fetchRoadmap(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch roadmap: %w", err)
	}

	// 2. Fetch open cascade:managed issues across all target repos
	existingIssues, err := ch.fetchManagedIssues(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch managed issues: %w", err)
	}

	// 3. Call Claude to diff roadmap against existing issues
	actions, costCents, err := ch.diffRoadmap(ctx, roadmap, existingIssues)
	if err != nil {
		return nil, fmt.Errorf("diff roadmap: %w", err)
	}

	// 4. Execute the actions
	result := &CascadeResult{
		Actions:   actions,
		CostCents: costCents,
		Model:     ch.model,
	}

	for _, action := range actions {
		var execErr error
		switch action.Type {
		case "create":
			execErr = ch.executeCreate(ctx, action)
			if execErr == nil {
				result.Created++
			}
		case "close":
			execErr = ch.executeClose(ctx, action)
			if execErr == nil {
				result.Closed++
			}
		case "relabel":
			execErr = ch.executeRelabel(ctx, action)
			if execErr == nil {
				result.Relabeled++
			}
		default:
			execErr = fmt.Errorf("unknown action type: %s", action.Type)
		}

		if execErr != nil {
			errMsg := fmt.Sprintf("%s %s: %v", action.Type, action.Repo, execErr)
			result.Errors = append(result.Errors, errMsg)
			fmt.Fprintf(os.Stderr, "[octi-pulpo] cascade error: %s\n", errMsg)
		}
	}

	fmt.Fprintf(os.Stderr, "[octi-pulpo] cascade complete: created=%d closed=%d relabeled=%d errors=%d\n",
		result.Created, result.Closed, result.Relabeled, len(result.Errors))

	return result, nil
}

// managedIssue represents an existing cascade:managed issue.
type managedIssue struct {
	Repo   string   `json:"repo"`
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
	State  string   `json:"state"`
}

func (ch *CascadeHandler) fetchRoadmap(ctx context.Context) (string, error) {
	// Try roadmap.md first, then strategy/ directory
	url := "https://api.github.com/repos/AgentGuardHQ/agentguard-workspace/contents/roadmap.md"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+ch.ghToken)
	req.Header.Set("Accept", "application/vnd.github.raw+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Fallback: try strategy/roadmap.md
		return ch.fetchFile(ctx, "AgentGuardHQ/agentguard-workspace", "strategy/roadmap.md")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (ch *CascadeHandler) fetchFile(ctx context.Context, repo, path string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", repo, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+ch.ghToken)
	req.Header.Set("Accept", "application/vnd.github.raw+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (ch *CascadeHandler) fetchManagedIssues(ctx context.Context) ([]managedIssue, error) {
	var allIssues []managedIssue

	for _, repo := range ch.targetRepos {
		issues, err := ch.fetchRepoManagedIssues(ctx, repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[octi-pulpo] cascade: failed to fetch issues from %s: %v\n", repo, err)
			continue // non-fatal — process other repos
		}
		allIssues = append(allIssues, issues...)
	}

	return allIssues, nil
}

func (ch *CascadeHandler) fetchRepoManagedIssues(ctx context.Context, repo string) ([]managedIssue, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues?state=open&labels=cascade:managed&per_page=100", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+ch.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var ghIssues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ghIssues); err != nil {
		return nil, err
	}

	var issues []managedIssue
	for _, gi := range ghIssues {
		var labels []string
		for _, l := range gi.Labels {
			labels = append(labels, l.Name)
		}
		issues = append(issues, managedIssue{
			Repo:   repo,
			Number: gi.Number,
			Title:  gi.Title,
			Body:   gi.Body,
			Labels: labels,
			State:  gi.State,
		})
	}
	return issues, nil
}

func (ch *CascadeHandler) diffRoadmap(ctx context.Context, roadmap string, existingIssues []managedIssue) ([]CascadeAction, int, error) {
	// Format existing issues for the prompt
	var issuesSummary strings.Builder
	if len(existingIssues) == 0 {
		issuesSummary.WriteString("(none)\n")
	}
	for _, issue := range existingIssues {
		issuesSummary.WriteString(fmt.Sprintf("- [%s #%d] %s (labels: %s)\n",
			issue.Repo, issue.Number, issue.Title, strings.Join(issue.Labels, ", ")))
	}

	prompt := fmt.Sprintf(`You are a strategy cascade agent. Your job is to keep GitHub issues in sync with the roadmap.

## Roadmap (source of truth)

%s

## Existing cascade:managed Issues

%s

## Target Repositories

%s

## Instructions

Compare the roadmap items against the existing cascade:managed issues. Determine:

1. **Create** — roadmap items that have no matching issue yet. Create them in the correct target repo.
2. **Close** — existing issues whose roadmap items are marked "done" or have been removed from the roadmap.
3. **Relabel** — existing issues whose priority changed (use labels like "priority:P0" through "priority:P3").

Rules:
- Only manage issues with the "cascade:managed" label — never touch issues without it.
- Match roadmap items to issues by title similarity and repo target.
- For new issues, write a clear title (prefixed with the roadmap category) and a body with context from the roadmap.
- When closing, reference the roadmap change as the reason.
- Be conservative — if unsure whether an item matches an existing issue, do NOT create a duplicate.
- Do NOT create issues for items marked "done" in the roadmap.
- Do NOT create issues for items already covered by an existing issue.

Respond with ONLY a JSON object:
{
  "actions": [
    {"type": "create", "repo": "AgentGuardHQ/...", "title": "...", "body": "..."},
    {"type": "close", "repo": "AgentGuardHQ/...", "issue_number": 123, "reason": "..."},
    {"type": "relabel", "repo": "AgentGuardHQ/...", "issue_number": 123, "labels": ["priority:P0", "cascade:managed"]}
  ]
}

If no actions are needed, respond with: {"actions": []}`,
		roadmap,
		issuesSummary.String(),
		strings.Join(ch.targetRepos, ", "))

	reqBody := map[string]interface{}{
		"model":      ch.model,
		"max_tokens": 4096,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("x-api-key", ch.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
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
		return nil, 0, fmt.Errorf("parse API response: %w", err)
	}
	if len(apiResp.Content) == 0 {
		return nil, 0, fmt.Errorf("empty API response")
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

	var cascadeResp struct {
		Actions []CascadeAction `json:"actions"`
	}
	if err := json.Unmarshal([]byte(rawText), &cascadeResp); err != nil {
		return nil, 0, fmt.Errorf("parse cascade response: %w (raw: %s)", err, rawText)
	}

	// Validate actions — only allow targeting known repos
	repoSet := make(map[string]bool)
	for _, r := range ch.targetRepos {
		repoSet[r] = true
	}

	var validActions []CascadeAction
	for _, action := range cascadeResp.Actions {
		if !repoSet[action.Repo] {
			fmt.Fprintf(os.Stderr, "[octi-pulpo] cascade: skipping action for unknown repo %s\n", action.Repo)
			continue
		}
		validActions = append(validActions, action)
	}

	// Estimate cost (Haiku: $0.80/MTok input, $4/MTok output)
	costCents := (apiResp.Usage.InputTokens*80 + apiResp.Usage.OutputTokens*400) / 1_000_000

	return validActions, costCents, nil
}

func (ch *CascadeHandler) executeCreate(ctx context.Context, action CascadeAction) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues", action.Repo)

	body := action.Body + "\n\n---\n_Created by Octi Pulpo strategy cascade_"
	reqBody, _ := json.Marshal(map[string]interface{}{
		"title":  action.Title,
		"body":   body,
		"labels": []string{"cascade:managed", "triage:needed"},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ch.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create issue API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var created struct {
		Number int `json:"number"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	fmt.Fprintf(os.Stderr, "[octi-pulpo] cascade: created %s#%d — %s\n",
		action.Repo, created.Number, action.Title)

	return nil
}

func (ch *CascadeHandler) executeClose(ctx context.Context, action CascadeAction) error {
	// Post a comment explaining the closure
	commentURL := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", action.Repo, action.IssueNumber)
	comment := fmt.Sprintf("Closing — %s\n\n_Closed by Octi Pulpo strategy cascade_", action.Reason)
	commentBody, _ := json.Marshal(map[string]string{"body": comment})

	commentReq, err := http.NewRequestWithContext(ctx, http.MethodPost, commentURL, bytes.NewReader(commentBody))
	if err != nil {
		return err
	}
	commentReq.Header.Set("Authorization", "Bearer "+ch.ghToken)
	commentReq.Header.Set("Accept", "application/vnd.github+json")
	commentReq.Header.Set("Content-Type", "application/json")

	commentResp, err := http.DefaultClient.Do(commentReq)
	if err != nil {
		return fmt.Errorf("post close comment: %w", err)
	}
	commentResp.Body.Close()

	// Close the issue
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", action.Repo, action.IssueNumber)
	reqBody, _ := json.Marshal(map[string]string{"state": "closed"})

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ch.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("close issue API returned %d: %s", resp.StatusCode, string(respBody))
	}

	fmt.Fprintf(os.Stderr, "[octi-pulpo] cascade: closed %s#%d — %s\n",
		action.Repo, action.IssueNumber, action.Reason)

	return nil
}

func (ch *CascadeHandler) executeRelabel(ctx context.Context, action CascadeAction) error {
	// Ensure cascade:managed is always in the label set
	hasCascade := false
	for _, l := range action.Labels {
		if l == "cascade:managed" {
			hasCascade = true
			break
		}
	}
	if !hasCascade {
		action.Labels = append(action.Labels, "cascade:managed")
	}

	// Replace all labels on the issue
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels", action.Repo, action.IssueNumber)
	reqBody, _ := json.Marshal(map[string][]string{"labels": action.Labels})

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ch.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("relabel issue API returned %d: %s", resp.StatusCode, string(respBody))
	}

	fmt.Fprintf(os.Stderr, "[octi-pulpo] cascade: relabeled %s#%d — %v\n",
		action.Repo, action.IssueNumber, action.Labels)

	return nil
}
