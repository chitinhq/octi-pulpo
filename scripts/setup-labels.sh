#!/usr/bin/env bash
# setup-labels.sh — Create pipeline stage labels across all AgentGuardHQ repos.
# Usage: bash scripts/setup-labels.sh [repo...]
# Default: all AgentGuardHQ repos
set -euo pipefail

LABELS=(
  "stage:architect|0E8A16|Pipeline: architect stage"
  "stage:implement|1D76DB|Pipeline: implementation stage"
  "stage:qa|FBCA04|Pipeline: QA stage"
  "stage:review|D93F0B|Pipeline: review stage"
  "stage:release|6F42C1|Pipeline: release stage"
  "fast-path|C5DEF5|Skips architect stage (trivial fix)"
)

if [ $# -gt 0 ]; then
  REPOS="$*"
else
  REPOS=$(gh repo list AgentGuardHQ --json name -q '.[].name')
fi

for repo in $REPOS; do
  echo "=== AgentGuardHQ/$repo ==="
  for entry in "${LABELS[@]}"; do
    IFS='|' read -r name color desc <<< "$entry"
    if gh label create "$name" --repo "AgentGuardHQ/$repo" --color "$color" --description "$desc" 2>/dev/null; then
      echo "  created: $name"
    else
      echo "  exists:  $name"
    fi
  done
  echo ""
done

echo "Done."
