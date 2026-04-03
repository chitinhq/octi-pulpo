package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/redis/go-redis/v9"
)

const (
	copilotFixComment   = "@copilot apply changes based on the comments in this thread"
	copilotEscalComment = "⚠️ Copilot fix loop: 2 attempts failed. Needs human review."
	defaultMaxAttempts  = 2
)

// copilotRedis is a narrow interface covering the Redis commands used by
// CopilotFixLoop. *redis.Client and *redis.ClusterClient both satisfy it,
// and it is easy to mock in tests without implementing the full redis.Cmdable.
type copilotRedis interface {
	Incr(ctx context.Context, key string) *redis.IntCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// CopilotFixLoop auto-triggers Copilot fixes when PR reviews request changes.
type CopilotFixLoop struct {
	ghToken     string
	rdb         copilotRedis // for tracking attempt counts
	baseURL     string       // GitHub API base URL (for testing)
	maxAttempts int
}

// NewCopilotFixLoop creates the fix loop handler.
func NewCopilotFixLoop(ghToken string, rdb redis.Cmdable) *CopilotFixLoop {
	return &CopilotFixLoop{
		ghToken:     ghToken,
		rdb:         rdb,
		baseURL:     "https://api.github.com",
		maxAttempts: defaultMaxAttempts,
	}
}

// HandleReview processes a pull_request_review event.
// If changes are requested, posts @copilot comment (up to maxAttempts).
// If approved or merged, resets the counter.
func (c *CopilotFixLoop) HandleReview(ctx context.Context, repo string, prNumber int, reviewState string) error {
	switch reviewState {
	case "approved":
		return c.ResetAttempts(ctx, repo, prNumber)
	case "changes_requested":
		return c.handleChangesRequested(ctx, repo, prNumber)
	default:
		// "commented" or any other state — do nothing
		return nil
	}
}

func (c *CopilotFixLoop) handleChangesRequested(ctx context.Context, repo string, prNumber int) error {
	key := c.attemptKey(repo, prNumber)

	// Increment attempt counter atomically
	count, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("copilot-fix: increment attempt counter: %w", err)
	}

	if count > int64(c.maxAttempts) {
		// Already posted escalation — do not spam
		return nil
	}

	if count == int64(c.maxAttempts) {
		// Exactly at the limit: post escalation comment
		return c.postComment(ctx, repo, prNumber, copilotEscalComment)
	}

	// Under the limit: post the @copilot trigger comment
	return c.postComment(ctx, repo, prNumber, copilotFixComment)
}

// ResetAttempts clears the fix counter (call when PR is approved or merged).
func (c *CopilotFixLoop) ResetAttempts(ctx context.Context, repo string, prNumber int) error {
	key := c.attemptKey(repo, prNumber)
	if err := c.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("copilot-fix: reset attempt counter: %w", err)
	}
	return nil
}

// postComment posts a comment on a PR via the GitHub API.
func (c *CopilotFixLoop) postComment(ctx context.Context, repo string, prNumber int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.baseURL, repo, prNumber)

	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("copilot-fix: marshal comment: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("copilot-fix: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("copilot-fix: post comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copilot-fix: GitHub API %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// attemptKey returns the Redis key for tracking fix attempts.
func (c *CopilotFixLoop) attemptKey(repo string, prNumber int) string {
	return "octi:copilot-fix:" + repo + ":" + strconv.Itoa(prNumber)
}
