#!/usr/bin/env bash
# pre-dispatch.sh — deterministic precondition checks before agent dispatch.
# Called by Octi dispatch loop. Exits non-zero to abort dispatch.
# Usage: pre-dispatch.sh <platform> <repo> <issue_number> <queue>
set -euo pipefail

PLATFORM="${1:?platform required (copilot|claude)}"
REPO="${2:?repo required}"
ISSUE_NUM="${3:?issue number required}"
QUEUE="${4:?queue required (intake|build|validate|groom)}"

WORKSPACE="${OCTI_WORKSPACE:-$HOME/workspace}"
REPO_DIR="$WORKSPACE/$REPO"

err() { echo "PRE-DISPATCH FAIL: $*" >&2; exit 1; }

# 1. Repo exists and is a git repo
[[ -d "$REPO_DIR/.git" ]] || err "repo $REPO_DIR is not a git repository"

# 2. Working tree is clean — no uncommitted changes that could leak into worktree
# Only flag modified/staged files as dirty — untracked files are fine
# Check this BEFORE branch recovery so we don't lose uncommitted work
DIRTY=$(git -C "$REPO_DIR" status --porcelain 2>/dev/null | grep -v '^??' | head -1 || true)
[[ -z "$DIRTY" ]] || err "repo $REPO has uncommitted changes: $DIRTY"

# 3. Repo is on default branch (main or master) — auto-recover stale branches
DEFAULT_BRANCH=$(git -C "$REPO_DIR" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|refs/remotes/origin/||' || echo "master")
CURRENT_BRANCH=$(git -C "$REPO_DIR" rev-parse --abbrev-ref HEAD)
if [[ "$CURRENT_BRANCH" != "$DEFAULT_BRANCH" ]]; then
  echo "PRE-DISPATCH WARN: repo $REPO on branch $CURRENT_BRANCH, auto-recovering to $DEFAULT_BRANCH" >&2
  git -C "$REPO_DIR" checkout "$DEFAULT_BRANCH" 2>/dev/null || err "repo $REPO is on branch $CURRENT_BRANCH and auto-checkout of $DEFAULT_BRANCH failed"
fi

# 4. No conflicting worktrees for this issue
WORKTREE_PATTERN="swarm/*-${ISSUE_NUM}"
EXISTING=$(git -C "$REPO_DIR" worktree list --porcelain 2>/dev/null | grep -c "$WORKTREE_PATTERN" || true)
[[ "$EXISTING" -eq 0 ]] || err "worktree already exists for issue $ISSUE_NUM"

# 5. Platform binary exists
case "$PLATFORM" in
  claude)  command -v claude >/dev/null 2>&1 || err "claude CLI not found in PATH" ;;
  copilot) command -v copilot >/dev/null 2>&1 || err "copilot CLI not found in PATH" ;;
  gemini)  command -v gemini >/dev/null 2>&1 || err "gemini CLI not found in PATH" ;;
  codex)   command -v codex >/dev/null 2>&1 || err "codex CLI not found in PATH" ;;
  *)       err "unknown platform: $PLATFORM" ;;
esac

# 6. No active interactive Claude session (for claude platform only)
if [[ "$PLATFORM" == "claude" && "${OCTI_SKIP_SESSION_CHECK:-}" != "1" ]]; then
  # Check for interactive Claude sessions by looking for claude processes with a TTY.
  # Skip processes in our own process group (swarm dispatches).
  INTERACTIVE_CLAUDE=$(pgrep -f "claude" 2>/dev/null | while read pid; do
    # Skip if it's in our process tree
    OUR_PGID=$(ps -o pgid= -p $$ 2>/dev/null | tr -d ' ')
    THEIR_PGID=$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d ' ')
    [[ "$OUR_PGID" == "$THEIR_PGID" ]] && continue
    # Check if it has a controlling TTY (interactive session)
    TTY=$(ps -o tty= -p "$pid" 2>/dev/null | tr -d ' ')
    [[ "$TTY" != "?" && -n "$TTY" ]] && echo "$pid"
  done || true)
  [[ -z "$INTERACTIVE_CLAUDE" ]] || err "interactive Claude session detected (pid: $INTERACTIVE_CLAUDE) — swarm paused"
fi

# 7. Issue exists and has expected labels for this queue
ISSUE_JSON=$(gh api "repos/chitinhq/$REPO/issues/$ISSUE_NUM" --jq '{state: .state, labels: [.labels[].name]}' 2>/dev/null) || err "cannot fetch issue #$ISSUE_NUM from $REPO"
ISSUE_STATE=$(echo "$ISSUE_JSON" | jq -r '.state')
[[ "$ISSUE_STATE" == "open" ]] || err "issue #$ISSUE_NUM is $ISSUE_STATE, expected open"

LABELS=$(echo "$ISSUE_JSON" | jq -r '.labels[]')
case "$QUEUE" in
  intake)
    echo "$LABELS" | grep -q "^planned$" && err "issue #$ISSUE_NUM already has 'planned' label — not in intake"
    ;;
  build)
    echo "$LABELS" | grep -q "^planned$" || err "issue #$ISSUE_NUM missing 'planned' label — not ready for build"
    echo "$LABELS" | grep -q "^implemented$" && err "issue #$ISSUE_NUM already has 'implemented' label"
    ;;
  validate)
    echo "$LABELS" | grep -q "^implemented$" || err "issue #$ISSUE_NUM missing 'implemented' label — not ready for validate"
    echo "$LABELS" | grep -q "^validated$" && err "issue #$ISSUE_NUM already validated"
    ;;
esac

# 8. Check for agent:claimed — someone else might be working on it
echo "$LABELS" | grep -q "^agent:claimed$" && err "issue #$ISSUE_NUM is already claimed by another agent"

echo "PRE-DISPATCH OK: $PLATFORM/$REPO#$ISSUE_NUM ($QUEUE)"
