package sprint

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// ghAvailable returns true if the gh CLI is installed and authenticated.
func ghAvailable() bool {
	out, err := exec.Command("gh", "auth", "status").CombinedOutput()
	return err == nil && strings.Contains(string(out), "Logged in")
}

func TestCloseIssue_RejectsInvalidRepo(t *testing.T) {
	if !ghAvailable() {
		t.Skip("gh not available or not authenticated")
	}
	ctx := context.Background()
	err := CloseIssue(ctx, "not-a-real-repo/does-not-exist", 99999, "")
	if err == nil {
		t.Fatal("expected error for non-existent repo, got nil")
	}
}

func TestCreateIssue_RejectsInvalidRepo(t *testing.T) {
	if !ghAvailable() {
		t.Skip("gh not available or not authenticated")
	}
	ctx := context.Background()
	_, err := CreateIssue(ctx, "not-a-real-repo/does-not-exist", "test title", "", "")
	if err == nil {
		t.Fatal("expected error for non-existent repo, got nil")
	}
}
