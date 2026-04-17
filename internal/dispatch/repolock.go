package dispatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrWorktreeRace is the sentinel marker for a failed `git worktree add`
// after we held the sidecar flock — the signature of a stale
// `.git/config.lock` or an otherwise-contended repo.
//
// Surface contract: adapters write result.Status="failed" and prefix
// result.Error with "worktree race:" (via fmt.Sprintf on ErrWorktreeRace.Error()).
// Downstream detectors (Sentinel telemetry) match on the STRING prefix —
// not errors.Is — because the value lives in a plain string field, not
// in an error return. If this ever needs to be machine-matchable via
// errors.Is, the adapter surface must change first to thread an error
// value alongside result.Error (see #TODO in adapters). Keeping it as a
// prefix for now because it's the cheapest observable change that gets
// Sentinel out of log-scrape forensics.
var ErrWorktreeRace = errors.New("worktree race")

// staleConfigLockTTL is how old `<repoPath>/.git/config.lock` must be
// before repoLock will opportunistically remove it. Measured from after
// we already hold the sidecar flock, so we never race our own siblings.
const staleConfigLockTTL = 60 * time.Second

// repoLock acquires an exclusive lock on a sidecar file
// (`<repoPath>/.git/octi-worktree.lock`) so concurrent adapter dispatches
// against the same repo can't race each other inside `git worktree add`.
//
// Git serializes its own writes to `.git/config` via `.git/config.lock`,
// but `git worktree add -b <branch>` writes upstream tracking into the
// parent repo's config — two parallel calls with overlapping config writes
// occasionally lose that race and exit 255 with
// "could not lock config file .git/config: File exists". This helper
// serializes the `worktree add` prelude per-repo at the OS level via
// flock(2), which works across processes (systemd timers can fork multiple
// dispatcher procs — a sync.Mutex wouldn't catch that).
//
// Scope must stay tight: release() before any long-running subprocess
// starts. Holding the flock across the 10-min clawta run would serialize
// all dispatch per-repo and tank throughput.
//
// While holding the flock, repoLock also opportunistically removes
// `<repoPath>/.git/config.lock` if it is older than staleConfigLockTTL —
// the canonical "previous run crashed" footprint that would otherwise
// still block us even with our own serialization in place.
func repoLock(repoPath string) (release func(), err error) {
	cleanRepo, err := sanitizeRepoPath(repoPath)
	if err != nil {
		return nil, err
	}

	// Require an existing git repo at repoPath. `os.MkdirAll` on `.git`
	// would happily create the directory inside an unrelated folder if
	// repoPath is mis-resolved — mutating state we don't own and masking
	// the real "not a git repo" error. Cheaper and safer: Stat the known
	// file `.git/config`; real git repos always have it (both plain repos
	// and worktrees), and it's present in all our adapter-managed checkouts.
	gitDir := filepath.Join(cleanRepo, ".git")
	if _, err := os.Stat(filepath.Join(gitDir, "config")); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("repoLock: %s is not a git repo (no .git/config)", cleanRepo)
		}
		return nil, fmt.Errorf("repoLock: stat .git/config: %w", err)
	}

	lockPath := filepath.Join(gitDir, "octi-worktree.lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("repoLock: open %s: %w", lockPath, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("repoLock: flock %s: %w", lockPath, err)
	}

	// Opportunistic stale-lock removal: now that we hold the sidecar
	// flock, any .git/config.lock that outlived its creator is safe to
	// remove — no concurrent sibling can race us on creating a new one.
	removeStaleConfigLock(gitDir)

	release = func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return release, nil
}

// removeStaleConfigLock deletes `<gitDir>/config.lock` if it is older than
// staleConfigLockTTL. Best-effort; errors are swallowed because the caller
// is about to try `git worktree add` anyway — git will surface any real
// problem. gitDir is trusted — it is `<sanitized repoPath>/.git`, built
// from the sanitizeRepoPath output, so no additional path validation here.
func removeStaleConfigLock(gitDir string) {
	configLock := filepath.Join(gitDir, "config.lock")
	info, err := os.Stat(configLock)
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) < staleConfigLockTTL {
		return
	}
	_ = os.Remove(configLock)
}

// sanitizeRepoPath normalizes and validates a repo path before it is used
// to derive filesystem paths inside the lock/stale-lock helpers. Adapter
// config is an internal surface, not end-user input, but this explicit
// sanitizer (a) keeps the trust boundary visible and (b) satisfies static
// analyzers that track filesystem taint.
//
// Requirements:
//   - non-empty
//   - cleaned (no duplicate separators, no trailing `.`/`..`)
//   - absolute (rejects accidental relative paths that could hit the cwd)
//   - no `..` segments surviving Clean (belt-and-braces against traversal)
//   - no NUL bytes (some filesystems silently truncate)
func sanitizeRepoPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("repoLock: empty repoPath")
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("repoLock: repoPath contains NUL byte")
	}
	cleaned := filepath.Clean(p)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("repoLock: repoPath must be absolute, got %q", p)
	}
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if seg == ".." {
			return "", fmt.Errorf("repoLock: repoPath contains traversal segment: %q", p)
		}
	}
	return cleaned, nil
}
