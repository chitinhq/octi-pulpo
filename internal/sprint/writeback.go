package sprint

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// CloseIssue closes a GitHub issue and posts an optional comment.
// comment may be empty to close without a comment.
func CloseIssue(ctx context.Context, repo string, issueNum int, comment string) error {
	args := []string{"issue", "close", "-R", repo, strconv.Itoa(issueNum)}
	if comment != "" {
		args = append(args, "--comment", comment)
	}
	out, err := exec.CommandContext(ctx, "gh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh issue close %s#%d: %w — %s", repo, issueNum, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CreateIssue creates a new GitHub issue and returns the issue number.
// labels is a comma-separated list of label names (may be empty).
func CreateIssue(ctx context.Context, repo, title, body, labels string) (int, error) {
	args := []string{
		"issue", "create",
		"-R", repo,
		"--title", title,
	}
	if body != "" {
		args = append(args, "--body", body)
	}
	if labels != "" {
		args = append(args, "--label", labels)
	}
	// gh issue create prints the URL of the new issue to stdout.
	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		return 0, fmt.Errorf("gh issue create in %s: %w", repo, err)
	}

	// Parse issue number from URL: https://github.com/owner/repo/issues/123
	url := strings.TrimSpace(string(out))
	parts := strings.Split(url, "/")
	if len(parts) == 0 {
		return 0, fmt.Errorf("unexpected gh output: %q", url)
	}
	numStr := parts[len(parts)-1]
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("parse issue number from %q: %w", url, err)
	}
	return num, nil
}
