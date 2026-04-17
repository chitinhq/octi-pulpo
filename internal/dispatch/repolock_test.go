package dispatch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// runGitIn is a tiny helper that runs `git <args...>` in dir and fails the
// test on error (tests only). Named with the `In` suffix to avoid collision
// with runGit in copilot_cli_adapter_silentloss_test.go.
func runGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, string(out))
	}
}

// newBareOriginAndRepo creates a bare "origin" repo plus a local clone with
// one commit on main, ready for `git worktree add -b <branch> origin/main`.
// Returns the path to the local clone.
func newBareOriginAndRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	bare := filepath.Join(root, "origin.git")
	runGitIn(t, root, "init", "--bare", "-b", "main", bare)

	// Seed: local clone, commit, push main so `origin/main` resolves.
	seed := filepath.Join(root, "seed")
	runGitIn(t, root, "clone", bare, seed)
	runGitIn(t, seed, "config", "user.email", "hopper@test")
	runGitIn(t, seed, "config", "user.name", "hopper")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runGitIn(t, seed, "add", ".")
	runGitIn(t, seed, "commit", "-m", "seed")
	// Some git versions default to master on init; ensure branch is main.
	runGitIn(t, seed, "branch", "-M", "main")
	runGitIn(t, seed, "push", "-u", "origin", "main")

	// The "repo" under test is a fresh clone that carries origin/main.
	repo := filepath.Join(root, "repo")
	runGitIn(t, root, "clone", bare, repo)
	return repo
}

// TestRepoLockFiveGoroutinesWorktreeAdd is the core race test for hopper's
// slice (thread item #2). Five goroutines contend on the same repoPath,
// each running the exact serialized prelude the adapters use:
//
//	repoLock -> git worktree add ... -> release
//
// Without the flock, parallel `git worktree add -b` calls occasionally exit
// 255 with "could not lock config file .git/config: File exists" (the
// ganglia-sr hourly silent-loss signature). With the flock, all 5 must
// succeed.
func TestRepoLockFiveGoroutinesWorktreeAdd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping race test in short mode")
	}
	repo := newBareOriginAndRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatalf("mkdir worktree root: %v", err)
	}

	const n = 5
	var (
		wg       sync.WaitGroup
		fails    atomic.Int32
		failMsgs = make(chan string, n)
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			branch := "hopper-race-" + string(rune('a'+i))
			wtPath := filepath.Join(worktreeRoot, branch)

			release, err := repoLock(repo)
			if err != nil {
				fails.Add(1)
				failMsgs <- "repoLock: " + err.Error()
				return
			}
			cmd := exec.Command("git", "worktree", "add", wtPath, "-b", branch, "origin/main")
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			release()
			if err != nil {
				fails.Add(1)
				failMsgs <- "git worktree add: " + err.Error() + ": " + string(out)
				return
			}
			if _, statErr := os.Stat(wtPath); statErr != nil {
				fails.Add(1)
				failMsgs <- "worktree missing: " + statErr.Error()
			}
		}()
	}
	wg.Wait()
	close(failMsgs)

	if got := fails.Load(); got != 0 {
		for msg := range failMsgs {
			t.Errorf("worktree add failure: %s", msg)
		}
		t.Fatalf("expected 0 failures across %d goroutines, got %d", n, got)
	}
}

// TestRepoLockSerializesSameRepo asserts two concurrent repoLock holders
// against the same repo do not overlap — a light invariant check that
// doesn't depend on git. If flock is doing its job, the total elapsed
// time is roughly hold * 2 (no overlap); if it isn't, it's ~hold.
func TestRepoLockSerializesSameRepo(t *testing.T) {
	tmp := t.TempDir()
	// Fake a .git dir + config file so repoLock's "is this a git repo" Stat
	// check passes. The lock contents don't matter; only the presence does.
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatalf("seed .git/config: %v", err)
	}

	const hold = 50 * time.Millisecond
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := repoLock(tmp)
			if err != nil {
				t.Errorf("repoLock: %v", err)
				return
			}
			time.Sleep(hold)
			rel()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	if elapsed < 2*hold-10*time.Millisecond {
		t.Fatalf("repoLock did not serialize: elapsed=%v (want >= ~%v)", elapsed, 2*hold)
	}
}

// TestRepoLockStaleConfigLockRemoved asserts that a >60s old
// `.git/config.lock` is cleared after repoLock returns — the opportunistic
// stale-lock removal that lets us recover from a crashed prior run.
func TestRepoLockStaleConfigLockRemoved(t *testing.T) {
	tmp := t.TempDir()
	gitDir := filepath.Join(tmp, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	// Seed .git/config so repoLock's is-a-git-repo guard passes.
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatalf("seed .git/config: %v", err)
	}
	configLock := filepath.Join(gitDir, "config.lock")
	if err := os.WriteFile(configLock, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed stale config.lock: %v", err)
	}
	// Backdate >60s.
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(configLock, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	rel, err := repoLock(tmp)
	if err != nil {
		t.Fatalf("repoLock: %v", err)
	}
	defer rel()

	if _, err := os.Stat(configLock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale config.lock removed; stat err=%v", err)
	}
}

// TestRepoLockFreshConfigLockKept asserts we do NOT clobber a recent
// `.git/config.lock` (<60s old) — some concurrent git process may be
// legitimately using it.
func TestRepoLockFreshConfigLockKept(t *testing.T) {
	tmp := t.TempDir()
	gitDir := filepath.Join(tmp, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	// Seed .git/config so repoLock's is-a-git-repo guard passes.
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatalf("seed .git/config: %v", err)
	}
	configLock := filepath.Join(gitDir, "config.lock")
	if err := os.WriteFile(configLock, []byte("fresh"), 0o644); err != nil {
		t.Fatalf("seed fresh config.lock: %v", err)
	}

	rel, err := repoLock(tmp)
	if err != nil {
		t.Fatalf("repoLock: %v", err)
	}
	defer rel()

	if _, err := os.Stat(configLock); err != nil {
		t.Fatalf("fresh config.lock unexpectedly removed: %v", err)
	}
}

// TestRepoLockRejectsNonGitRepo pins the safety guard from Copilot's PR #277
// review: repoLock must NOT create `.git/` inside an unrelated directory when
// repoPath is mis-resolved. Instead, it rejects with a clear
// "not a git repo" error by Stat'ing `.git/config`.
func TestRepoLockRejectsNonGitRepo(t *testing.T) {
	tmp := t.TempDir() // empty dir, no .git/config
	_, err := repoLock(tmp)
	if err == nil {
		t.Fatalf("expected error for non-git-repo path, got nil")
	}
	if !strings.Contains(err.Error(), "not a git repo") {
		t.Fatalf("error message should identify the cause; got: %v", err)
	}
	// Confirm we did NOT create the .git dir as a side effect.
	if _, statErr := os.Stat(filepath.Join(tmp, ".git")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf(".git dir should not be created on rejection; stat err=%v", statErr)
	}
}

// TestSanitizeRepoPath rejects the inputs a caller must never pass while
// accepting a normal absolute workspace path. Paired with the dispatch
// adapters' call sites: repoPath flows from internal adapter config, but
// the sanitizer pins the contract so a future caller can't regress it.
func TestSanitizeRepoPath(t *testing.T) {
	tmp := t.TempDir() // guaranteed absolute, clean

	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"clean absolute", tmp, false},
		{"redundant slashes", tmp + "/./", false},
		// filepath.Clean resolves `..` inside an absolute path, so a traversal
		// like "<tmp>/../etc" normalizes to "/etc" and does NOT leave a
		// surviving `..` segment. Covered here as the accept case so we pin
		// the Clean behavior — the traversal guard is belt-and-braces for
		// exotic filesystems where Clean may not collapse.
		{"clean collapses traversal", tmp + "/sub/../other", false},
		{"empty", "", true},
		{"relative", "relative/path", true},
		{"nul byte", tmp + "\x00/bad", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sanitizeRepoPath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if !filepath.IsAbs(got) {
				t.Fatalf("sanitized path not absolute: %q", got)
			}
		})
	}
}

// TestErrWorktreeRaceIsDefined pins the exported sentinel so downstream
// code (sentinel telemetry) can match on it via errors.Is.
func TestErrWorktreeRaceIsDefined(t *testing.T) {
	if ErrWorktreeRace == nil {
		t.Fatal("ErrWorktreeRace must be a non-nil sentinel")
	}
	if ErrWorktreeRace.Error() == "" {
		t.Fatal("ErrWorktreeRace.Error() must be non-empty")
	}
}
