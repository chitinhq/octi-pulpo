package dispatch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	gitRunner   gitRunner           // optional: git command runner override for tests
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
		ghToken:   ghToken,
		apiKey:    apiKey,
		model:     model,
		gitRunner: execGitRunner{},
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

	// Apply the fixes if they were implemented.
	if result.Implemented && len(result.Files) > 0 {
		if applyErr := c.applyFixes(ctx, repo, prNumber, prMeta, result); applyErr != nil {
			fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to apply fixes for PR #%d: %v\n", prNumber, applyErr)
			if commentErr := c.postComment(ctx, repo, prNumber, fmt.Sprintf(
				"## Tier B Coding: Fix Application Failed\n\n%s\n\n**Error:** %v\n\nThe branch was not updated automatically.",
				result.Changes, applyErr)); commentErr != nil {
				fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to post error comment on PR #%d: %v\n", prNumber, commentErr)
			}
			if labelErr := c.addLabel(ctx, repo, prNumber, "agent:stuck"); labelErr != nil {
				fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to add agent:stuck label to PR #%d: %v\n", prNumber, labelErr)
			}
			return result, fmt.Errorf("apply fixes: %w", applyErr)
		}

		comment := fmt.Sprintf(
			"## Tier B Coding: Fixes Applied\n\n**Summary:** %s\n\n**Changes:**\n%s\n\nFiles updated:\n%s",
			result.Summary,
			result.Changes,
			formatCodingFiles(result.Files),
		)
		if postErr := c.postComment(ctx, repo, prNumber, comment); postErr != nil {
			fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to post comment on PR #%d: %v\n", prNumber, postErr)
		}
		if removeErr := c.removeLabel(ctx, repo, prNumber, "tier:b-code"); removeErr != nil {
			fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to remove tier:b-code label from PR #%d: %v\n", prNumber, removeErr)
		}
		if addErr := c.addLabel(ctx, repo, prNumber, "tier:b-fixed"); addErr != nil {
			fmt.Fprintf(os.Stderr, "[octi-pulpo] failed to add tier:b-fixed label to PR #%d: %v\n", prNumber, addErr)
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
		Head struct {
			Ref  string `json:"ref"`
			Repo struct {
				CloneURL string `json:"clone_url"`
			} `json:"repo"`
		} `json:"head"`
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
		HeadRef:   pr.Head.Ref,
		CloneURL:  pr.Head.Repo.CloneURL,
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

// applyFixes applies the generated fixes to the PR branch by cloning the
// repository, checking out the PR branch, writing the updated files, committing
// the changes, and pushing the branch back to origin.
func (c *CodingHandler) applyFixes(ctx context.Context, repo string, prNumber int, pr *prMetadata, result *CodingResult) error {
	if pr == nil {
		return fmt.Errorf("missing PR metadata")
	}
	if pr.HeadRef == "" {
		return fmt.Errorf("missing PR branch")
	}
	if pr.CloneURL == "" {
		return fmt.Errorf("missing PR clone URL")
	}

	authArgs := gitAuthArgs(c.ghToken)

	tempDir, err := os.MkdirTemp("", "octi-coding-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	worktreeDir := filepath.Join(tempDir, "repo")
	fmt.Fprintf(os.Stderr, "[octi-pulpo] applying fixes for PR #%d in %s\n", prNumber, repo)

	cloneArgs := append(append([]string{}, authArgs...), "clone", pr.CloneURL, worktreeDir)
	if out, err := c.runGit(ctx, "", cloneArgs...); err != nil {
		return fmt.Errorf("clone repo: %s", redactGitOutput(out, err))
	}
	if out, err := c.runGit(ctx, worktreeDir, "checkout", pr.HeadRef); err != nil {
		return fmt.Errorf("checkout branch: %s", redactGitOutput(out, err))
	}

	for _, file := range result.Files {
		if err := writeCodingFile(worktreeDir, file); err != nil {
			return fmt.Errorf("apply file %s: %w", file.Path, err)
		}
	}

	if out, err := c.runGit(ctx, worktreeDir, "add", "-A"); err != nil {
		return fmt.Errorf("stage changes: %s", redactGitOutput(out, err))
	}

	changed, err := c.runGit(ctx, worktreeDir, "diff", "--cached", "--name-only")
	if err != nil {
		return fmt.Errorf("inspect staged changes: %s", redactGitOutput(changed, err))
	}
	if strings.TrimSpace(string(changed)) == "" {
		return fmt.Errorf("no changes produced by fix application")
	}

	if out, err := c.runGit(ctx, worktreeDir,
		"-c", "user.name=Octi Pulpo",
		"-c", "user.email=noreply@chitinhq.com",
		"commit",
		"-m", "fix: apply Tier B coding fixes",
		"-m", "Co-Authored-By: Octi Pulpo <noreply@chitinhq.com>",
	); err != nil {
		return fmt.Errorf("commit changes: %s", redactGitOutput(out, err))
	}

	pushArgs := append(append([]string{}, authArgs...), "push", "origin", "HEAD:"+pr.HeadRef)
	if out, err := c.runGit(ctx, worktreeDir, pushArgs...); err != nil {
		return fmt.Errorf("push changes: %s", redactGitOutput(out, err))
	}

	return nil
}

type gitRunner interface {
	CombinedOutput(ctx context.Context, dir string, args ...string) ([]byte, error)
}

type execGitRunner struct{}

func (execGitRunner) CombinedOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func (c *CodingHandler) runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	runner := c.gitRunner
	if runner == nil {
		runner = execGitRunner{}
	}
	return runner.CombinedOutput(ctx, dir, args...)
}

// gitAuthArgs returns git CLI flags that inject the GitHub token via
// http.extraheader instead of embedding it in the clone URL. This avoids
// leaking the token via `ps` output or `.git/config`.
func gitAuthArgs(token string) []string {
	if token == "" {
		return nil
	}
	cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{"-c", "http.extraheader=Authorization: basic " + cred}
}

// credentialPattern matches tokens, passwords, and basic-auth strings that
// should be scrubbed from git output before surfacing in error messages.
var credentialPattern = regexp.MustCompile(`(?i)(basic\s+[A-Za-z0-9+/=]+|x-access-token:[^\s@]+|://[^\s@]+@)`)

// redactGitOutput combines git's stderr/stdout with the exit error and
// redacts any credential material so it is safe to include in logs or
// user-visible error messages.
func redactGitOutput(out []byte, err error) string {
	msg := ""
	if len(out) > 0 {
		msg = strings.TrimSpace(credentialPattern.ReplaceAllString(string(out), "[REDACTED]"))
	}
	if err != nil {
		if msg != "" {
			msg += ": "
		}
		msg += err.Error()
	}
	return msg
}

func writeCodingFile(root string, file CodingResultFile) error {
	cleanPath := filepath.Clean(file.Path)
	if cleanPath == "." || filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("invalid file path %q", file.Path)
	}

	targetPath := filepath.Join(root, cleanPath)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}

	// Preserve existing file permissions; fall back to 0644 for new files.
	mode := os.FileMode(0o644)
	info, err := os.Stat(targetPath)
	if err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(targetPath, []byte(file.Fixed), mode)
}

func formatCodingFiles(files []CodingResultFile) string {
	if len(files) == 0 {
		return "- none"
	}

	var b strings.Builder
	for _, file := range files {
		b.WriteString("- ")
		b.WriteString(file.Path)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}
