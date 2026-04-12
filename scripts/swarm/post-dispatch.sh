#!/usr/bin/env bash
# post-dispatch.sh — deterministic result validation after agent completes.
# Called by Octi dispatch loop. Exits non-zero if agent output is invalid.
# Usage: post-dispatch.sh <platform> <repo> <issue_number> <queue> <worktree_dir> <exit_code>
set -euo pipefail

PLATFORM="${1:?platform required}"
REPO="${2:?repo required}"
ISSUE_NUM="${3:?issue number required}"
QUEUE="${4:?queue required}"
WORKTREE_DIR="${5:?worktree dir required}"
EXIT_CODE="${6:?exit code required}"

WORKSPACE="${OCTI_WORKSPACE:-$HOME/workspace}"
REPO_DIR="$WORKSPACE/$REPO"

warn() { echo "POST-DISPATCH WARN: $*" >&2; }
fail() { echo "POST-DISPATCH FAIL: $*" >&2; RESULT="failed"; }

RESULT="success"

# 1. Check agent exit code
if [[ "$EXIT_CODE" -ne 0 ]]; then
  fail "agent exited with code $EXIT_CODE"
fi

# Resolve `chitin` binary for gate invocation. Falls back to PATH if the
# workspace build isn't present, which keeps this script portable.
CHITIN_BIN="${CHITIN_BIN:-$WORKSPACE/chitin/chitin}"
if [[ ! -x "$CHITIN_BIN" ]]; then
  CHITIN_BIN="$(command -v chitin 2>/dev/null || true)"
fi

# run_chitin_gate <gate-name> invokes the given Chitin Gate with the current
# dispatch context. Exit 0 = pass (issue can advance), 1 = fail (artifact
# didn't meet criteria), 2 = error (stalls, does not advance).
run_chitin_gate() {
  local gate_name="$1"
  if [[ -z "$CHITIN_BIN" ]]; then
    warn "chitin binary not found — skipping gate $gate_name (install chitin to enable)"
    return 0
  fi
  "$CHITIN_BIN" gate run "$gate_name" \
    --repo "$REPO" --issue "$ISSUE_NUM" --queue "$QUEUE" \
    --worktree "$WORKTREE_DIR" --format human
  return $?
}

# 2. Queue-specific validation
case "$QUEUE" in
  intake)
    # Plan queue: agent should have produced a plan comment with acceptance
    # criteria. The existing warn-only comment-count check is kept as a
    # quick cheap sanity check; the Chitin Gate is the authoritative quality
    # bar and fails the post-dispatch if criteria are missing.
    COMMENT_COUNT=$(gh api "repos/chitinhq/$REPO/issues/$ISSUE_NUM/comments" --jq 'length' 2>/dev/null || echo "0")
    [[ "$COMMENT_COUNT" -gt 0 ]] || warn "no plan comment found on issue #$ISSUE_NUM"

    run_chitin_gate planning/check_acceptance_criteria
    gate_rc=$?
    case "$gate_rc" in
      0) ;; # pass — nothing to do
      1) fail "planning gate: acceptance criteria missing or insufficient" ;;
      *) fail "planning gate errored (rc=$gate_rc) — issue stalled for review" ;;
    esac
    ;;

  build)
    # Build queue: commits present, build+tests pass, no secrets. All
    # delegated to the Chitin build gate so the logic is consistent
    # whether invoked here, from CI, or from a local pre-commit check.
    if [[ ! -d "$WORKTREE_DIR" ]]; then
      fail "worktree $WORKTREE_DIR does not exist"
    else
      run_chitin_gate build/check_compile
      gate_rc=$?
      case "$gate_rc" in
        0) ;; # pass
        1) fail "build gate: commits missing, build/tests failed, or secrets in diff" ;;
        *) fail "build gate errored (rc=$gate_rc)" ;;
      esac
    fi
    ;;

  validate)
    # Validate queue: CI must be green on the PR before we let the issue
    # advance to "validated". The Chitin Gate resolves the PR, inspects
    # the most recent workflow run, and returns fail if CI is red or
    # error if CI is still in-flight (so the issue stalls for the next
    # tick rather than bouncing).
    run_chitin_gate validate/check_ci_passed
    gate_rc=$?
    case "$gate_rc" in
      0) ;; # pass — CI is green
      1) fail "validate gate: CI did not pass on linked PR" ;;
      *) fail "validate gate errored (rc=$gate_rc) — CI may still be running" ;;
    esac

    # Keep the review-comment warning as a soft signal for human eyes.
    PR_NUM=$(gh api "repos/chitinhq/$REPO/pulls?state=open&head=chitinhq:swarm/build-$ISSUE_NUM" --jq '.[0].number' 2>/dev/null || echo "")
    if [[ -n "$PR_NUM" ]]; then
      REVIEW_COUNT=$(gh api "repos/chitinhq/$REPO/pulls/$PR_NUM/reviews" --jq 'length' 2>/dev/null || echo "0")
      [[ "$REVIEW_COUNT" -gt 0 ]] || warn "no review found on PR #$PR_NUM"
    fi
    ;;
esac

# 3. Output result as JSON for Octi to consume
cat <<EOF
{
  "result": "$RESULT",
  "platform": "$PLATFORM",
  "repo": "$REPO",
  "issue": $ISSUE_NUM,
  "queue": "$QUEUE",
  "exit_code": $EXIT_CODE
}
EOF

[[ "$RESULT" == "success" ]] && exit 0 || exit 1
