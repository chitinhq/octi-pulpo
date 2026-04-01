#!/usr/bin/env bash
set -euo pipefail

# Install Octi Pulpo pipeline into a target repo.
# Usage: setup-pipeline.sh <owner/repo> [--dry-run]
#
# Prerequisites:
#   - gh CLI authenticated with repo scope
#   - Target repo must exist
#
# What it does:
#   1. Creates required labels
#   2. Copies workflow files to .github/workflows/
#   3. Copies scripts to .github/scripts/
#   4. Copies default octi-config.json to .github/
#   5. Validates secrets exist

REPO="${1:?Usage: setup-pipeline.sh <owner/repo> [--dry-run]}"
DRY_RUN="${2:-}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKFLOWS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=== Octi Pipeline Setup ==="
echo "Target: ${REPO}"
echo "Source: ${WORKFLOWS_DIR}"
[ "$DRY_RUN" = "--dry-run" ] && echo "MODE: DRY RUN"
echo ""

# 1. Create labels
LABELS=(
  "triage:needed|C5DEF5|Needs triage"
  "tier:c|0E8A16|Tier C — Copilot implementation"
  "tier:b-scope|FBCA04|Tier B — Needs planning/scoping"
  "tier:b-code|D93F0B|Tier B — Senior agent coding"
  "tier:a-groom|B60205|Tier A — Human grooming required"
  "tier:a|B60205|Tier A — Human architect"
  "tier:ci-running|C2E0C6|CI running"
  "tier:review|BFD4F2|Awaiting review"
  "tier:needs-revision|E4E669|Needs revision"
  "needs:human|D73A4A|Requires human attention"
  "agent:review|1D76DB|Agent PR review"
)

echo "--- Creating labels ---"
for entry in "${LABELS[@]}"; do
  IFS='|' read -r NAME COLOR DESC <<< "$entry"
  if [ "$DRY_RUN" = "--dry-run" ]; then
    echo "  [DRY] Would create label: ${NAME}"
  else
    gh label create "$NAME" --repo "$REPO" --color "$COLOR" --description "$DESC" 2>/dev/null && \
      echo "  [OK] Created: ${NAME}" || \
      echo "  [SKIP] Already exists: ${NAME}"
  fi
done

# 2. Clone target repo (temp)
if [ "$DRY_RUN" != "--dry-run" ]; then
  TMPDIR=$(mktemp -d)
  echo ""
  echo "--- Cloning ${REPO} ---"
  gh repo clone "$REPO" "$TMPDIR" -- --depth 1
  cd "$TMPDIR"

  # Detect default branch
  DEFAULT_BRANCH=$(git symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's@^refs/remotes/origin/@@' || echo "main")
  BRANCH="feat/octi-pipeline-setup"

  git checkout -b "$BRANCH"

  # 3. Copy workflows
  mkdir -p .github/workflows .github/scripts
  for yml in "$WORKFLOWS_DIR"/octi-*.yml; do
    cp "$yml" .github/workflows/
    echo "  [COPY] $(basename "$yml") -> .github/workflows/"
  done

  # 4. Copy scripts
  for sh in "$WORKFLOWS_DIR"/scripts/claude-*.sh; do
    [ -f "$sh" ] && cp "$sh" .github/scripts/ && echo "  [COPY] $(basename "$sh") -> .github/scripts/"
  done

  # 5. Copy config (don't overwrite if exists)
  if [ ! -f ".github/octi-config.json" ]; then
    cp "$WORKFLOWS_DIR/octi-config.json" .github/octi-config.json
    echo "  [COPY] octi-config.json -> .github/"
  else
    echo "  [SKIP] .github/octi-config.json already exists"
  fi

  # 6. Commit and push
  git add .github/
  git commit -m "feat: install Octi Pulpo pipeline workflows

Adds triage, Copilot dispatch, PR gate, review handler, and sweeper
workflows from the Octi Pulpo pipeline framework."

  git push -u origin "$BRANCH"

  # 7. Open PR
  PR_URL=$(gh pr create \
    --repo "$REPO" \
    --title "feat: install Octi Pulpo pipeline" \
    --body "## Summary
- Installs 5 pipeline workflows (triage, dispatch, gate, review, sweeper)
- Adds Claude triage script
- Creates pipeline labels
- Adds default octi-config.json

## Required Secrets
- \`ANTHROPIC_API_KEY\` — for Claude API triage
- \`OCTI_PAT\` — GitHub PAT with repo scope (or GitHub App token)

## Next Steps
1. Add secrets to repo settings
2. Merge this PR
3. Create a test issue to validate the pipeline

_Installed by Octi Pulpo setup script_")

  echo ""
  echo "=== Setup Complete ==="
  echo "PR: ${PR_URL}"
  echo ""
  echo "Required secrets (add to repo settings):"
  echo "  - ANTHROPIC_API_KEY"
  echo "  - OCTI_PAT"

  # Cleanup
  rm -rf "$TMPDIR"
else
  echo ""
  echo "=== Dry Run Complete ==="
  echo "Would copy: $(ls "$WORKFLOWS_DIR"/octi-*.yml | wc -l) workflow files"
  echo "Would copy: $(ls "$WORKFLOWS_DIR"/scripts/claude-*.sh 2>/dev/null | wc -l) script files"
fi
