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

# Special case: "workspace" repo IS the workspace directory itself
if [[ "$REPO" == "workspace" ]]; then
  REPO_DIR="$WORKSPACE"
fi
MCP_CONFIG="$WORKSPACE/octi/mcp-swarm.json"
LOG_DIR="${OCTI_LOG_DIR:-$HOME/.local/share/octi/swarm}"

mkdir -p "$LOG_DIR"

log() { echo "[$(date -u +%H:%M:%S)] $*"; }
die() { log "ABORT: $*" >&2; exit 1; }

# ── Flow emit helpers ────────────────────────────────────────────────
# Thin wrappers around `chitin flow emit`. Errors never abort dispatch:
# if chitin isn't installed or the subcommand fails, we log nothing
# and continue. Fields are passed via the FIELDS env var as a
# space-separated list of key=value pairs to avoid quoting pain.
_flow_field_args() {
  local args=()
  if [[ -n "${FIELDS:-}" ]]; then
    for kv in $FIELDS; do
      args+=(--field "$kv")
    done
  fi
  printf '%s\n' "${args[@]}"
}
flow_start() {
  local name="$1"
  local field_args=()
  while IFS= read -r line; do [[ -n "$line" ]] && field_args+=("$line"); done < <(_flow_field_args)
  chitin flow emit "$name" started "${field_args[@]}" >/dev/null 2>&1 || true
}
flow_complete() {
  local name="$1"
  local field_args=()
  while IFS= read -r line; do [[ -n "$line" ]] && field_args+=("$line"); done < <(_flow_field_args)
  chitin flow emit "$name" completed "${field_args[@]}" >/dev/null 2>&1 || true
}
flow_fail() {
  local name="$1"
  local reason="$2"
  local field_args=()
  while IFS= read -r line; do [[ -n "$line" ]] && field_args+=("$line"); done < <(_flow_field_args)
  chitin flow emit "$name" failed --reason "$reason" "${field_args[@]}" >/dev/null 2>&1 || true
}

# Generate a dispatch_id for this run (used as a correlation key across
# all 12 phase events). Falls back to epoch+pid if uuidgen is missing.
DISPATCH_ID="${DISPATCH_ID:-$(uuidgen 2>/dev/null || echo "dispatch-$(date +%s)-$$")}"
export DISPATCH_ID

# Ensure the parent span always closes, even on die/failure paths.
# Guarded so it's safe to call before the span is opened (no-op in that
# case because _flow_dispatch_opened stays 0).
_flow_dispatch_opened=0
_flow_dispatch_closed=0
close_flow_dispatch() {
  local rc=$?
  if [[ "$_flow_dispatch_opened" -eq 1 && "$_flow_dispatch_closed" -eq 0 ]]; then
    _flow_dispatch_closed=1
    if [[ "$rc" -eq 0 ]]; then
      FIELDS="dispatch_id=$DISPATCH_ID repo=$REPO issue=$ISSUE_NUM platform=$PLATFORM queue=$QUEUE" \
        flow_complete swarm.dispatch
    else
      FIELDS="dispatch_id=$DISPATCH_ID repo=$REPO issue=$ISSUE_NUM platform=$PLATFORM queue=$QUEUE exit_code=$rc" \
        flow_fail swarm.dispatch "dispatch exited rc=$rc"
    fi
  fi
}

# ── Phase 0: Open Chitin session ─────────────────────────────────────
# The session wraps the entire dispatch so every downstream event
# (pre-check, gate, telemetry) threads the same session id. If chitin
# isn't installed we fall back to an empty id and the rest of the
# script works unchanged — session_id in downstream JSON just ends up
# blank rather than failing the dispatch.
CHITIN_BIN="${CHITIN_BIN:-$(command -v chitin 2>/dev/null || true)}"
CHITIN_SESSION_ID=""
if [[ -n "$CHITIN_BIN" ]]; then
  # Capture the active soul if any — not strictly needed since chitin
  # will inherit automatically, but explicit fingerprinting is clearer
  # in the persisted session record.
  ACTIVE_SOUL=$("$CHITIN_BIN" soul status --format json 2>/dev/null | \
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('soul',''))" 2>/dev/null || echo "")
  CHITIN_SESSION_ID=$("$CHITIN_BIN" session start \
    --driver "$PLATFORM" \
    --model "$MODEL" \
    --role "$QUEUE" \
    --soul "$ACTIVE_SOUL" 2>/dev/null | tail -1 || echo "")
  if [[ -n "$CHITIN_SESSION_ID" ]]; then
    log "Opened chitin session $CHITIN_SESSION_ID"
  fi
fi
export CHITIN_SESSION_ID

# Ensure the session is always closed on exit, even on `die` failure
# paths. The EXIT trap runs under set -e without disturbing the
# final exit code.
cleanup_chitin_session() {
  local rc=$?
  # Close the parent flow span first so its timing doesn't include
  # the chitin session teardown.
  close_flow_dispatch
  if [[ -n "${CHITIN_SESSION_ID:-}" && -n "${CHITIN_BIN:-}" ]]; then
    local reason="dispatch_done"
    [[ "$rc" -ne 0 ]] && reason="dispatch_failed_rc${rc}"
    "$CHITIN_BIN" session end --id "$CHITIN_SESSION_ID" --reason "$reason" >/dev/null 2>&1 || true
  fi
}
trap cleanup_chitin_session EXIT

# Open the parent span that brackets the whole dispatch. Placed after
# the trap so close_flow_dispatch always runs to close it.
FIELDS="dispatch_id=$DISPATCH_ID repo=$REPO issue=$ISSUE_NUM platform=$PLATFORM queue=$QUEUE model=$MODEL" \
  flow_start swarm.dispatch
_flow_dispatch_opened=1

# ── Phase 1: Deterministic pre-checks (no tokens) ────────────────────
log "Phase 1: Pre-dispatch checks"
FIELDS="dispatch_id=$DISPATCH_ID" flow_start swarm.dispatch.phase.pre_checks

if ! "$SCRIPT_DIR/check-budget.sh" "$PLATFORM"; then
  FIELDS="dispatch_id=$DISPATCH_ID" flow_fail swarm.dispatch.phase.pre_checks "budget check failed"
  die "budget check failed"
fi
if ! "$SCRIPT_DIR/pre-dispatch.sh" "$PLATFORM" "$REPO" "$ISSUE_NUM" "$QUEUE"; then
  FIELDS="dispatch_id=$DISPATCH_ID" flow_fail swarm.dispatch.phase.pre_checks "pre-dispatch check failed"
  die "pre-dispatch check failed"
fi
FIELDS="dispatch_id=$DISPATCH_ID" flow_complete swarm.dispatch.phase.pre_checks

# ── Phase 2: Build prompt from template (no tokens) ──────────────────
log "Phase 2: Building prompt from template"
FIELDS="dispatch_id=$DISPATCH_ID" flow_start swarm.dispatch.phase.prompt_building

if ! PROMPT=$("$SCRIPT_DIR/build-prompt.sh" "$REPO" "$ISSUE_NUM" "$QUEUE"); then
  FIELDS="dispatch_id=$DISPATCH_ID" flow_fail swarm.dispatch.phase.prompt_building "prompt build failed"
  die "prompt build failed"
fi
PROMPT_LEN=${#PROMPT}
log "Prompt assembled: $PROMPT_LEN chars"
FIELDS="dispatch_id=$DISPATCH_ID prompt_len=$PROMPT_LEN" flow_complete swarm.dispatch.phase.prompt_building

# ── Phase 3: Claim issue ─────────────────────────────────────────────
log "Phase 3: Claiming issue #$ISSUE_NUM"
FIELDS="dispatch_id=$DISPATCH_ID" flow_start swarm.dispatch.phase.claim

gh api "repos/chitinhq/$REPO/issues/$ISSUE_NUM/labels" --input - <<< "{\"labels\":[\"agent:claimed\"]}" >/dev/null 2>&1 || true
FIELDS="dispatch_id=$DISPATCH_ID" flow_complete swarm.dispatch.phase.claim

# ── Phase 4: Create worktree ─────────────────────────────────────────
BRANCH="swarm/${QUEUE}-${ISSUE_NUM}"
WORKTREE_DIR="$REPO_DIR/.worktrees/$BRANCH"

if [[ "$QUEUE" != "intake" && "$QUEUE" != "groom" ]]; then
  log "Phase 4: Creating worktree $BRANCH"
  FIELDS="dispatch_id=$DISPATCH_ID branch=$BRANCH" flow_start swarm.dispatch.phase.worktree
  if ! git -C "$REPO_DIR" worktree add -b "$BRANCH" "$WORKTREE_DIR" 2>/dev/null; then
    FIELDS="dispatch_id=$DISPATCH_ID branch=$BRANCH" flow_fail swarm.dispatch.phase.worktree "worktree creation failed"
    die "worktree creation failed"
  fi
  WORK_DIR="$WORKTREE_DIR"
  FIELDS="dispatch_id=$DISPATCH_ID branch=$BRANCH" flow_complete swarm.dispatch.phase.worktree
else
  # Intake and groom don't need worktrees — they read, not write.
  # Emit a started/completed pair anyway so the phase is observable and
  # dashboards see a 0ms span rather than a gap.
  FIELDS="dispatch_id=$DISPATCH_ID skipped=true queue=$QUEUE" flow_start swarm.dispatch.phase.worktree
  WORK_DIR="$REPO_DIR"
  FIELDS="dispatch_id=$DISPATCH_ID skipped=true queue=$QUEUE" flow_complete swarm.dispatch.phase.worktree
fi

# ── Phase 5: Dispatch agent (tokens spent here) ──────────────────────
log "Phase 5: Dispatching $PLATFORM ($MODEL) for $QUEUE"
FIELDS="dispatch_id=$DISPATCH_ID platform=$PLATFORM model=$MODEL queue=$QUEUE" \
  flow_start swarm.dispatch.phase.agent_execution
DISPATCH_START=$(date +%s%N)

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
    # Chitin hooks enforce governance — skip Claude's permission system
    ARGS=(-p "$PROMPT" --model "$MODEL" --dangerously-skip-permissions --max-turns "$MAX_TURNS" --output-format json)
    [[ -f "$MCP_CONFIG" ]] && ARGS+=(--mcp-config "$MCP_CONFIG")

    # Unset ANTHROPIC_API_KEY so claude -p uses Max plan OAuth, not a stale API key
    (unset ANTHROPIC_API_KEY && cd "$WORK_DIR" && claude "${ARGS[@]}") > "$OUTPUT_FILE" 2>&1 || EXIT_CODE=$?
    ;;

  copilot)
    ARGS=(-p "$PROMPT" --model "$MODEL" --yolo --no-ask-user --silent --output-format json --max-autopilot-continues "$MAX_TURNS")
    [[ -f "$MCP_CONFIG" ]] && ARGS+=(--additional-mcp-config "@$MCP_CONFIG")

    (cd "$WORK_DIR" && copilot "${ARGS[@]}") > "$OUTPUT_FILE" 2>&1 || EXIT_CODE=$?
    ;;

  gemini)
    # Gemini CLI: -p for headless, -y for auto-approve (chitin hooks govern), -o json for structured output
    ARGS=(-p "$PROMPT" -m "$MODEL" -y -o json)

    (cd "$WORK_DIR" && gemini "${ARGS[@]}") > "$OUTPUT_FILE" 2>&1 || EXIT_CODE=$?
    ;;

  codex)
    # Codex CLI: exec for headless, -m for model
    # Codex uses config for approval mode — set full-auto via -c
    ARGS=(exec "$PROMPT" -m "$MODEL" -c 'approval_mode="full-auto"' --json)

    (cd "$WORK_DIR" && codex "${ARGS[@]}") > "$OUTPUT_FILE" 2>&1 || EXIT_CODE=$?
    ;;
esac

DISPATCH_END=$(date +%s%N)
DURATION_MS=$(( (DISPATCH_END - DISPATCH_START) / 1000000 ))
log "Agent finished with exit code $EXIT_CODE (${DURATION_MS}ms)"

if [[ "$EXIT_CODE" -eq 0 ]]; then
  FIELDS="dispatch_id=$DISPATCH_ID duration_ms=$DURATION_MS exit_code=$EXIT_CODE" \
    flow_complete swarm.dispatch.phase.agent_execution
else
  FIELDS="dispatch_id=$DISPATCH_ID duration_ms=$DURATION_MS exit_code=$EXIT_CODE" \
    flow_fail swarm.dispatch.phase.agent_execution "agent exited rc=$EXIT_CODE"
fi

# ── Phase 6: Deterministic post-validation (no tokens) ───────────────
log "Phase 6: Post-dispatch validation"
FIELDS="dispatch_id=$DISPATCH_ID" flow_start swarm.dispatch.phase.post_validation

POST_RESULT=0
"$SCRIPT_DIR/post-dispatch.sh" "$PLATFORM" "$REPO" "$ISSUE_NUM" "$QUEUE" "${WORKTREE_DIR:-$REPO_DIR}" "$EXIT_CODE" || POST_RESULT=$?

# tests_passed / lint_passed aren't yet plumbed out of post-dispatch.sh
# as separate signals — it returns a single exit code. Emit what we
# have and mark both as derived from POST_RESULT so dashboards can
# still group. Splitting them properly is a follow-up on post-dispatch.sh.
if [[ "$POST_RESULT" -eq 0 ]]; then
  FIELDS="dispatch_id=$DISPATCH_ID tests_passed=true lint_passed=true post_result=$POST_RESULT" \
    flow_complete swarm.dispatch.phase.post_validation
else
  FIELDS="dispatch_id=$DISPATCH_ID tests_passed=false lint_passed=false post_result=$POST_RESULT" \
    flow_fail swarm.dispatch.phase.post_validation "post-validation failed rc=$POST_RESULT"
fi

# ── Phase 7: Advance labels or escalate ──────────────────────────────
log "Phase 7: Label advancement"
FIELDS="dispatch_id=$DISPATCH_ID from_label=agent:claimed" \
  flow_start swarm.dispatch.phase.label_advance

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
  FIELDS="dispatch_id=$DISPATCH_ID from_label=agent:claimed to_label=${NEXT_LABEL:-none}" \
    flow_complete swarm.dispatch.phase.label_advance
else
  log "Dispatch failed — escalation needed"
  RESULT="failed"
  FIELDS="dispatch_id=$DISPATCH_ID from_label=agent:claimed to_label=agent:blocked" \
    flow_fail swarm.dispatch.phase.label_advance "dispatch failed; escalation"
fi

# ── Phase 8: Log dispatch event ──────────────────────────────────────
FIELDS="dispatch_id=$DISPATCH_ID result=$RESULT" flow_start swarm.dispatch.phase.telemetry_emit
"$SCRIPT_DIR/log-dispatch.sh" "$PLATFORM" "$REPO" "$ISSUE_NUM" "$QUEUE" "$MODEL" "$RESULT"
FIELDS="dispatch_id=$DISPATCH_ID result=$RESULT" flow_complete swarm.dispatch.phase.telemetry_emit

# ── Phase 9: Emit telemetry for sentinel ──────────────────────────────
# Note: per spec, sentinel_eval is the phase name for the sentinel step
# (Phase 10 below). The emit-telemetry.sh call is folded under
# telemetry_emit (Phase 8) conceptually — we leave the log line here
# but don't double-emit a flow event.
log "Phase 9: Emitting telemetry"
"$SCRIPT_DIR/emit-telemetry.sh" "$PLATFORM" "$REPO" "$ISSUE_NUM" "$QUEUE" "$MODEL" "$RESULT" "$EXIT_CODE" "$DURATION_MS" || true

# ── Phase 10: Sentinel evaluation ────────────────────────────────────
log "Phase 10: Sentinel eval"
FIELDS="dispatch_id=$DISPATCH_ID" flow_start swarm.dispatch.phase.sentinel_eval
"$SCRIPT_DIR/sentinel-eval.sh" 2>&1 | tail -10 || true
FIELDS="dispatch_id=$DISPATCH_ID" flow_complete swarm.dispatch.phase.sentinel_eval

# ── Phase 10b: PR creation ───────────────────────────────────────────
# dispatch.sh doesn't currently run `gh pr create` itself — that's done
# by the build agent during Phase 5, or by downstream handlers. We emit
# a zero-duration span so the phase appears in dashboards; pr_number
# is unknown at this layer. Splitting PR creation out of the agent is
# tracked as a follow-up.
FIELDS="dispatch_id=$DISPATCH_ID note=pr_creation_inside_agent" \
  flow_start swarm.dispatch.phase.pr_creation
FIELDS="dispatch_id=$DISPATCH_ID note=pr_creation_inside_agent" \
  flow_complete swarm.dispatch.phase.pr_creation

# ── Phase 11: Cleanup worktree ───────────────────────────────────────
FIELDS="dispatch_id=$DISPATCH_ID" flow_start swarm.dispatch.phase.cleanup
if [[ -d "${WORKTREE_DIR:-}" ]]; then
  if [[ "$RESULT" == "success" && "$QUEUE" == "build" ]]; then
    log "Worktree kept for PR: $WORKTREE_DIR"
  else
    git -C "$REPO_DIR" worktree remove "$WORKTREE_DIR" --force 2>/dev/null || true
    log "Worktree cleaned up"
  fi
fi
FIELDS="dispatch_id=$DISPATCH_ID" flow_complete swarm.dispatch.phase.cleanup

# ── Phase 12: Ensure main checkout is on default branch ─────────────
FIELDS="dispatch_id=$DISPATCH_ID" flow_start swarm.dispatch.phase.ensure_main
DEFAULT_BRANCH=$(git -C "$REPO_DIR" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|refs/remotes/origin/||' || true)
if [[ -z "$DEFAULT_BRANCH" ]]; then
  if git -C "$REPO_DIR" rev-parse --verify origin/main &>/dev/null; then
    DEFAULT_BRANCH="main"
  else
    DEFAULT_BRANCH="master"
  fi
fi
CURRENT=$(git -C "$REPO_DIR" rev-parse --abbrev-ref HEAD 2>/dev/null || true)
if [[ "$CURRENT" != "$DEFAULT_BRANCH" ]]; then
  git -C "$REPO_DIR" checkout "$DEFAULT_BRANCH" 2>/dev/null || log "WARN: failed to return to $DEFAULT_BRANCH"
fi
FIELDS="dispatch_id=$DISPATCH_ID default_branch=$DEFAULT_BRANCH" \
  flow_complete swarm.dispatch.phase.ensure_main

log "Done: $PLATFORM/$REPO#$ISSUE_NUM ($QUEUE) → $RESULT"
exit $([[ "$RESULT" == "success" ]] && echo 0 || echo 1)
