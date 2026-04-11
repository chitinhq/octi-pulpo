package dispatch

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type gitCall struct {
	dir  string
	args []string
}

type mockGitRunner struct {
	calls       []gitCall
	cloneDest   string
	snapshotDir string
	failPush    bool
	diffOutput  string
}

// gitSubcommand finds the first non-flag argument (skipping -c key=val pairs)
// to determine the actual git subcommand.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++ // skip the value
			continue
		}
		return args[i]
	}
	return ""
}

func (m *mockGitRunner) CombinedOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	_ = ctx
	call := gitCall{dir: dir, args: append([]string(nil), args...)}
	m.calls = append(m.calls, call)

	if len(args) == 0 {
		return nil, nil
	}

	switch gitSubcommand(args) {
	case "clone":
		m.cloneDest = args[len(args)-1]
		if err := os.MkdirAll(m.cloneDest, 0o755); err != nil {
			return nil, err
		}
		return []byte("cloned"), nil
	case "checkout", "add":
		return nil, nil
	case "diff":
		if m.diffOutput == "" {
			return []byte("internal/dispatch/test.go\n"), nil
		}
		return []byte(m.diffOutput), nil
	case "commit":
		return []byte("committed"), nil
	case "push":
		if m.failPush {
			return []byte("fatal: could not push"), os.ErrPermission
		}
		if m.snapshotDir != "" && m.cloneDest != "" {
			if err := copyTree(m.cloneDest, m.snapshotDir); err != nil {
				return nil, err
			}
		}
		return []byte("pushed"), nil
	default:
		return nil, nil
	}
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func TestCodingHandlerApplyFixes_WritesFilesAndPushes(t *testing.T) {
	snapshotDir := t.TempDir()
	runner := &mockGitRunner{snapshotDir: snapshotDir}
	handler := &CodingHandler{
		ghToken:   "",
		gitRunner: runner,
	}
	pr := &prMetadata{
		HeadRef:  "feature/fix-coding",
		CloneURL: "https://github.com/chitinhq/octi.git",
	}
	result := &CodingResult{
		Files: []CodingResultFile{
			{
				Path:  "internal/dispatch/generated.go",
				Fixed: "package dispatch\n\nconst answer = 42\n",
			},
		},
	}

	if err := handler.applyFixes(context.Background(), "chitinhq/octi", 168, pr, result); err != nil {
		t.Fatalf("applyFixes: %v", err)
	}

	if runner.cloneDest == "" {
		t.Fatal("clone destination not recorded")
	}

	got, err := os.ReadFile(filepath.Join(snapshotDir, "internal/dispatch/generated.go"))
	if err != nil {
		t.Fatalf("read fixed file: %v", err)
	}
	if string(got) != result.Files[0].Fixed {
		t.Fatalf("fixed file mismatch:\nwant: %q\ngot:  %q", result.Files[0].Fixed, string(got))
	}

	if len(runner.calls) != 6 {
		t.Fatalf("expected 6 git calls, got %d", len(runner.calls))
	}
	if got := runner.calls[0].args; len(got) != 3 || got[0] != "clone" {
		t.Fatalf("first call = %v, want git clone", got)
	}
	if got := runner.calls[1].args; len(got) != 2 || got[0] != "checkout" || got[1] != pr.HeadRef {
		t.Fatalf("second call = %v, want git checkout %s", got, pr.HeadRef)
	}
	if got := runner.calls[2].args; len(got) != 2 || got[0] != "add" || got[1] != "-A" {
		t.Fatalf("third call = %v, want git add -A", got)
	}
	if got := runner.calls[3].args; len(got) != 3 || got[0] != "diff" || got[1] != "--cached" || got[2] != "--name-only" {
		t.Fatalf("fourth call = %v, want git diff --cached --name-only", got)
	}
	if got := runner.calls[4].args; len(got) < 5 || got[0] != "-c" || got[4] != "commit" {
		t.Fatalf("fifth call = %v, want git -c ... commit", got)
	}
	if joined := strings.Join(runner.calls[4].args, " "); !strings.Contains(joined, "Co-Authored-By: Octi Pulpo <noreply@chitinhq.com>") {
		t.Fatalf("commit message missing trailer: %v", runner.calls[4].args)
	}
	if got := runner.calls[5].args; len(got) != 3 || got[0] != "push" || got[1] != "origin" || got[2] != "HEAD:"+pr.HeadRef {
		t.Fatalf("sixth call = %v, want git push origin HEAD:%s", got, pr.HeadRef)
	}
}

func TestCodingHandlerApplyFixes_PushFailure(t *testing.T) {
	runner := &mockGitRunner{failPush: true}
	handler := &CodingHandler{
		gitRunner: runner,
	}
	pr := &prMetadata{
		HeadRef:  "feature/fix-coding",
		CloneURL: "https://github.com/chitinhq/octi.git",
	}
	result := &CodingResult{
		Files: []CodingResultFile{
			{
				Path:  "internal/dispatch/generated.go",
				Fixed: "package dispatch\n",
			},
		},
	}

	err := handler.applyFixes(context.Background(), "chitinhq/octi", 168, pr, result)
	if err == nil {
		t.Fatal("applyFixes: expected error")
	}
	if !strings.Contains(err.Error(), "push changes") {
		t.Fatalf("applyFixes error = %v, want push failure", err)
	}
}
