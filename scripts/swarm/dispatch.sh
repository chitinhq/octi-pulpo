#!/usr/bin/env bash
# dispatch.sh — deterministic swarm dispatch wrapper.
# Runs pre-checks, builds prompt from template, dispatches agent, validates result.
# Usage: dispatch.sh <platform> <repo> <issue_number> <queue> <model>
set -euo pipefail

PLATFORM="${1:?platform required (copilot|claude)}"
REPO="${2:?repo required}"
ISSUE_NUM="${3:?issue number required}"
QUEUE="${4:?queue required (intake|build|validate|groom)}"
MODEL="${5:?model required}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKSPACE="${OCTI_WORKSPACE:-$HOME/workspace}"
REPO_DIR="$WORKSPACE/$REPO"
MCP_CONFIG="$WORKSPACE/octi/mcp-swarm.json"
LOG_DIR="${OCTI_LOG_DIR:-$HOME/.local/share/octi/swarm}"

mkdir -p "$LOG_DIR"

log() { echo "[$(date -u +%H:%M:%S)] $*"; }
die() { log "ABORT: $*" >&2; exit 1; }

# ── Phase 1: Deterministic pre-checks (no tokens) ────────────────────
log "Phase 1: Pre-dispatch checks"

"$SCRIPT_DIR/check-budget.sh" "$PLATFORM" || die "budget check failed"
"$SCRIPT_DIR/pre-dispatch.sh" "$PLATFORM" "$REPO" "$ISSUE_NUM" "$QUEUE" || die "pre-dispatch check failed"

# ── Phase 2: Build prompt from template (no tokens) ──────────────────
log "Phase 2: Building prompt from template"

PROMPT=$("$SCRIPT_DIR/build-prompt.sh" "$REPO" "$ISSUE_NUM" "$QUEUE") || die "prompt build failed"
PROMPT_LEN=${#PROMPT}
log "Prompt assembled: $PROMPT_LEN chars"

# ── Phase 3: Claim issue ─────────────────────────────────────────────
log "Phase 3: Claiming issue #$ISSUE_NUM"

gh api "repos/chitinhq/$REPO/issues/$ISSUE_NUM/labels" --input - <<< "{\"labels\":[\"agent:claimed\"]}" >/dev/null 2>&1 || true

# ── Phase 4: Create worktree ─────────────────────────────────────────
BRANCH="swarm/${QUEUE}-${ISSUE_NUM}"
WORKTREE_DIR="$REPO_DIR/.worktrees/$BRANCH"

if [[ "$QUEUE" != "intake" && "$QUEUE" != "groom" ]]; then
  log "Phase 4: Creating worktree $BRANCH"
  git -C "$REPO_DIR" worktree add -b "$BRANCH" "$WORKTREE_DIR" 2>/dev/null || die "worktree creation failed"
  WORK_DIR="$WORKTREE_DIR"
else
  # Intake and groom don't need worktrees — they read, not write
  WORK_DIR="$REPO_DIR"
fi

# ── Phase 5: Dispatch agent (tokens spent here) ──────────────────────
log "Phase 5: Dispatching $PLATFORM ($MODEL) for $QUEUE"

# Max turns by queue (deterministic, not LLM-decided)
case "$QUEUE" in
  groom)    MAX_TURNS=30 ;;
  intake)   MAX_TURNS=20 ;;
  build)    MAX_TURNS=80 ;;
  validate) MAX_TURNS=30 ;;
  *)        MAX_TURNS=50 ;;
esac

EXIT_CODE=0
OUTPUT_FILE="$LOG_DIR/${REPO}-${ISSUE_NUM}-${QUEUE}-$(date +%s).log"

case "$PLATFORM" in
  claude)
    # Use 'accept' permission mode so the agent can run gh commands for labels/comments
    ARGS=(-p "$PROMPT" --model "$MODEL" --permission-mode accept --max-turns "$MAX_TURNS" --output-format json)
    [[ -f "$MCP_CONFIG" ]] && ARGS+=(--mcp-config "$MCP_CONFIG")

    # Unset ANTHROPIC_API_KEY so claude -p uses Max plan OAuth, not a stale API key
    (unset ANTHROPIC_API_KEY && cd "$WORK_DIR" && claude "${ARGS[@]}") > "$OUTPUT_FILE" 2>&1 || EXIT_CODE=$?
    ;;

  copilot)
    ARGS=(-p "$PROMPT" --model "$MODEL" --yolo --no-ask-user --silent --output-format json --max-autopilot-continues "$MAX_TURNS")
    [[ -f "$MCP_CONFIG" ]] && ARGS+=(--additional-mcp-config "@$MCP_CONFIG")

    (cd "$WORK_DIR" && copilot "${ARGS[@]}") > "$OUTPUT_FILE" 2>&1 || EXIT_CODE=$?
    ;;
esac

log "Agent finished with exit code $EXIT_CODE"

# ── Phase 6: Deterministic post-validation (no tokens) ───────────────
log "Phase 6: Post-dispatch validation"

POST_RESULT=0
"$SCRIPT_DIR/post-dispatch.sh" "$PLATFORM" "$REPO" "$ISSUE_NUM" "$QUEUE" "${WORKTREE_DIR:-$REPO_DIR}" "$EXIT_CODE" || POST_RESULT=$?

# ── Phase 7: Advance labels or escalate ──────────────────────────────
log "Phase 7: Label advancement"

# Remove agent:claimed
gh api "repos/chitinhq/$REPO/issues/$ISSUE_NUM/labels/agent:claimed" -X DELETE >/dev/null 2>&1 || true

if [[ "$EXIT_CODE" -eq 0 && "$POST_RESULT" -eq 0 ]]; then
  case "$QUEUE" in
    intake)   NEXT_LABEL="planned" ;;
    build)    NEXT_LABEL="implemented" ;;
    validate) NEXT_LABEL="validated" ;;
    *)        NEXT_LABEL="" ;;
  esac
  if [[ -n "${NEXT_LABEL:-}" ]]; then
    gh api "repos/chitinhq/$REPO/issues/$ISSUE_NUM/labels" --input - <<< "{\"labels\":[\"$NEXT_LABEL\"]}" >/dev/null 2>&1
    log "Advanced: $QUEUE → $NEXT_LABEL"
  fi
  RESULT="success"
else
  log "Dispatch failed — escalation needed"
  RESULT="failed"
fi

# ── Phase 8: Log dispatch event ──────────────────────────────────────
"$SCRIPT_DIR/log-dispatch.sh" "$PLATFORM" "$REPO" "$ISSUE_NUM" "$QUEUE" "$MODEL" "$RESULT"

# ── Phase 9: Cleanup worktree ────────────────────────────────────────
if [[ -d "${WORKTREE_DIR:-}" ]]; then
  if [[ "$RESULT" == "success" && "$QUEUE" == "build" ]]; then
    log "Worktree kept for PR: $WORKTREE_DIR"
  else
    git -C "$REPO_DIR" worktree remove "$WORKTREE_DIR" --force 2>/dev/null || true
    log "Worktree cleaned up"
  fi
fi

log "Done: $PLATFORM/$REPO#$ISSUE_NUM ($QUEUE) → $RESULT"
exit $([[ "$RESULT" == "success" ]] && echo 0 || echo 1)
