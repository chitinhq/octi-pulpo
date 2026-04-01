#!/usr/bin/env bash
set -euo pipefail

# Install Octi Pulpo pipeline into a target repo.
# Usage: setup-pipeline.sh <owner/repo> [--prefix <prefix>] [--dry-run]
#
# Options:
#   --prefix <name>  Rename workflow files from 'octi-' to '<name>-' and
#                    rebrand internal references. Default: octi
#   --dry-run        Show what would be done without making changes
#
# Examples:
#   setup-pipeline.sh AgentGuardHQ/agentguard-cloud
#   setup-pipeline.sh myorg/frontend --prefix amd
#   setup-pipeline.sh myorg/frontend --prefix amd --dry-run
#
# Prerequisites:
#   - gh CLI authenticated with repo scope
#   - Target repo must exist

# Parse arguments
REPO=""
PREFIX="octi"
DRY_RUN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix) PREFIX="$2"; shift 2 ;;
    --dry-run) DRY_RUN="--dry-run"; shift ;;
    -*) echo "Unknown option: $1"; exit 1 ;;
    *) REPO="$1"; shift ;;
  esac
done

if [ -z "$REPO" ]; then
  echo "Usage: setup-pipeline.sh <owner/repo> [--prefix <prefix>] [--dry-run]"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKFLOWS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Brand name derived from prefix (for comments and PR text)
BRAND="${PREFIX^} Pipeline"  # capitalize first letter
[ "$PREFIX" = "octi" ] && BRAND="Octi Pulpo pipeline"

echo "=== ${BRAND} Setup ==="
echo "Target: ${REPO}"
echo "Prefix: ${PREFIX}-"
echo "Source: ${WORKFLOWS_DIR}"
[ -n "$DRY_RUN" ] && echo "MODE: DRY RUN"
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
  if [ -n "$DRY_RUN" ]; then
    echo "  [DRY] Would create label: ${NAME}"
  else
    gh label create "$NAME" --repo "$REPO" --color "$COLOR" --description "$DESC" 2>/dev/null && \
      echo "  [OK] Created: ${NAME}" || \
      echo "  [SKIP] Already exists: ${NAME}"
  fi
done

# 2. Clone target repo (temp)
if [ -z "$DRY_RUN" ]; then
  TMPDIR=$(mktemp -d)
  trap 'rm -rf "$TMPDIR"' EXIT
  echo ""
  echo "--- Cloning ${REPO} ---"
  gh repo clone "$REPO" "$TMPDIR" -- --depth 1
  cd "$TMPDIR"

  # Detect default branch
  DEFAULT_BRANCH=$(git symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's@^refs/remotes/origin/@@' || echo "main")
  BRANCH="feat/${PREFIX}-pipeline-setup"

  git checkout -b "$BRANCH"

  # 3. Copy workflows — rename octi- prefix to chosen prefix
  mkdir -p .github/workflows
  for yml in "$WORKFLOWS_DIR"/octi-*.yml; do
    BASENAME=$(basename "$yml")
    TARGET_NAME="${BASENAME/octi-/${PREFIX}-}"
    # Replace internal branding references
    sed \
      -e "s/Octi — /$(echo "${PREFIX^}") — /g" \
      -e "s/Octi Pulpo pipeline/${BRAND}/g" \
      -e "s/Octi Pulpo sweeper/${BRAND} sweeper/g" \
      -e "s/Octi PR Gate/$(echo "${PREFIX^}") PR Gate/g" \
      -e "s/Octi Review/$(echo "${PREFIX^}") Review/g" \
      -e "s/octi-sweeper/${PREFIX}-sweeper/g" \
      -e "s/octi-triage/${PREFIX}-triage/g" \
      -e "s/octi-pr-gate/${PREFIX}-pr-gate/g" \
      -e "s/octi-review/${PREFIX}-review/g" \
      "$yml" > ".github/workflows/${TARGET_NAME}"
    echo "  [COPY] ${BASENAME} -> .github/workflows/${TARGET_NAME}"
  done

  # 4. Copy config — update prefix field
  CONFIG_FILE=".github/${PREFIX}-config.json"
  if [ ! -f "$CONFIG_FILE" ]; then
    jq --arg prefix "$PREFIX" '. + {prefix: $prefix}' "$WORKFLOWS_DIR/octi-config.json" > "$CONFIG_FILE"
    echo "  [COPY] octi-config.json -> .github/${PREFIX}-config.json"
  else
    echo "  [SKIP] ${CONFIG_FILE} already exists"
  fi

  # 6. Commit and push
  git add .github/
  git commit -m "feat: install ${BRAND} workflows

Adds triage, Copilot dispatch, PR gate, review handler, and sweeper
workflows (prefix: ${PREFIX}-)."

  git push -u origin "$BRANCH"

  # 7. Open PR
  PR_URL=$(gh pr create \
    --repo "$REPO" \
    --title "feat: install ${BRAND}" \
    --body "## Summary
- Installs 5 pipeline workflows (prefix: \`${PREFIX}-\`)
- Adds Claude triage script
- Creates pipeline labels
- Adds default ${PREFIX}-config.json

## Secrets
- \`OCTI_PAT\` — org-level GitHub PAT or App token (for cross-repo label ops)
- No Claude API keys needed — AI calls are handled by Octi Pulpo on your Linux box

## Next Steps
1. Verify org-level secrets are configured
2. Merge this PR
3. Create a test issue to validate the pipeline

_Installed by ${BRAND} setup script_")

  echo ""
  echo "=== Setup Complete ==="
  echo "PR: ${PR_URL}"
  echo ""
  echo "Required: OCTI_PAT (org-level) for cross-repo operations."
  echo "No Claude API keys needed in GitHub — Octi Pulpo handles AI calls locally."
else
  echo ""
  echo "=== Dry Run Complete ==="
  echo "Prefix: ${PREFIX}-"
  echo "Would copy and rename: $(ls "$WORKFLOWS_DIR"/octi-*.yml | wc -l) workflow files"
  echo ""
  echo "File renames:"
  for yml in "$WORKFLOWS_DIR"/octi-*.yml; do
    BASENAME=$(basename "$yml")
    echo "  ${BASENAME} -> ${BASENAME/octi-/${PREFIX}-}"
  done
fi
