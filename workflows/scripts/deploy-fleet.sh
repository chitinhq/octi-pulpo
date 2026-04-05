#!/usr/bin/env bash
# Deploy Octi Pulpo pipeline to all AgentGuardHQ repos.
# Usage: deploy-fleet.sh [--dry-run]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DRY_RUN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN="--dry-run"; shift ;;
    -h|--help)
      echo "Usage: deploy-fleet.sh [--dry-run]"
      echo ""
      echo "Deploys Octi Pulpo pipeline to all AgentGuardHQ repos by calling"
      echo "setup-pipeline.sh for each repo with the appropriate --lang flag."
      echo ""
      echo "Options:"
      echo "  --dry-run   Show what would be done without making changes"
      echo "  -h, --help  Show this help"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# ── Target repos ────────────────────────────────────────────────
# Format: repo|default_branch|language
FLEET=(
  "AgentGuardHQ/agentguard|main|go"
  "AgentGuardHQ/octi-pulpo|main|go"
  "AgentGuardHQ/shellforge|main|go"
  "AgentGuardHQ/clawta|main|go"
  "AgentGuardHQ/sentinel|main|go"
  "AgentGuardHQ/llmint|main|go"
  "AgentGuardHQ/agentguard-analytics|main|python"
  "AgentGuardHQ/agentguard-cloud|main|typescript"
  "AgentGuardHQ/agentguard-workspace|master|docs"
  "AgentGuardHQ/agentguard-extensions|master|mixed"
  "AgentGuardHQ/preflight|master|go"
  "AgentGuardHQ/homebrew-tap|main|ruby"
)

# ── Track results ───────────────────────────────────────────────
TOTAL=${#FLEET[@]}
SUCCESS=0
FAILED=0
SKIPPED=0
declare -a RESULTS=()

echo "============================================"
echo "  Octi Pulpo Fleet Deployment"
echo "============================================"
echo "Repos:    ${TOTAL}"
echo "Mode:     ${DRY_RUN:-LIVE}"
echo "Time:     $(date -u +"%Y-%m-%dT%H:%M:%SZ")"
echo "============================================"
echo ""

for entry in "${FLEET[@]}"; do
  IFS='|' read -r REPO BRANCH LANG <<< "$entry"
  REPO_SHORT="${REPO#*/}"

  echo "--- [${REPO_SHORT}] (${LANG}, default: ${BRANCH}) ---"

  # Build setup-pipeline.sh arguments
  SETUP_ARGS=("$REPO")
  if [ -n "$DRY_RUN" ]; then
    SETUP_ARGS+=("$DRY_RUN")
  fi

  if [ -n "$DRY_RUN" ]; then
    echo "  [DRY] Would run: setup-pipeline.sh ${SETUP_ARGS[*]}"
    echo "  [DRY] Language: ${LANG}"
    echo "  [DRY] Default branch: ${BRANCH}"
    RESULTS+=("[DRY] ${REPO_SHORT} (${LANG})")
    SKIPPED=$((SKIPPED + 1))
  else
    if "${SCRIPT_DIR}/setup-pipeline.sh" "${SETUP_ARGS[@]}"; then
      echo "  [OK] ${REPO_SHORT} deployed successfully"
      RESULTS+=("[OK]   ${REPO_SHORT} (${LANG})")
      SUCCESS=$((SUCCESS + 1))
    else
      echo "  [FAIL] ${REPO_SHORT} deployment failed"
      RESULTS+=("[FAIL] ${REPO_SHORT} (${LANG})")
      FAILED=$((FAILED + 1))
    fi
  fi
  echo ""
done

# ── Summary ─────────────────────────────────────────────────────
echo "============================================"
echo "  Fleet Deployment Summary"
echo "============================================"
echo ""

for result in "${RESULTS[@]}"; do
  echo "  ${result}"
done

echo ""
echo "--------------------------------------------"
if [ -n "$DRY_RUN" ]; then
  echo "  DRY RUN: ${SKIPPED} repos would be deployed"
else
  echo "  Success: ${SUCCESS} / ${TOTAL}"
  echo "  Failed:  ${FAILED} / ${TOTAL}"
fi
echo "--------------------------------------------"

# Exit non-zero if any deployments failed
if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
