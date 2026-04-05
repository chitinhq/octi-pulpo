#!/usr/bin/env bash
# Scan all chitinhq repos for stale issues and close them.
# Usage: stale-issue-cleanup.sh [--dry-run] [--days <N>]
set -euo pipefail

DRY_RUN=""
STALE_DAYS=30
REPORT_FILE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN="true"; shift ;;
    --days) STALE_DAYS="$2"; shift 2 ;;
    -h|--help)
      echo "Usage: stale-issue-cleanup.sh [--dry-run] [--days <N>]"
      echo ""
      echo "Scans all chitinhq repos for stale issues (no activity"
      echo "in the last N days) and closes them with a standard comment."
      echo ""
      echo "Options:"
      echo "  --dry-run      Report but do not close issues"
      echo "  --days <N>     Stale threshold in days (default: 30)"
      echo "  -h, --help     Show this help"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# ── Target repos ────────────────────────────────────────────────
REPOS=(
  "chitinhq/agentguard-cloud"
  "chitinhq/agentguard"
  "chitinhq/octi-pulpo"
  "chitinhq/shellforge"
  "chitinhq/agentguard-analytics"
  "chitinhq/agentguard-extensions"
  "chitinhq/preflight"
  "chitinhq/homebrew-tap"
  "chitinhq/agentguard-workspace"
)

# Labels that protect an issue from being closed
PROTECTED_LABELS=(
  "do-not-close"
  "tier:c"
  "tier:b-scope"
  "tier:b-code"
  "tier:a-groom"
  "tier:a"
  "tier:ci-running"
  "tier:review"
  "tier:needs-revision"
)

STALE_COMMENT="This issue has been automatically closed due to inactivity (no updates in ${STALE_DAYS}+ days).

If this issue is still relevant, please reopen it and add the \`do-not-close\` label to prevent future auto-closure.

_Closed by Octi Pulpo stale issue cleanup._"

# ── Cutoff date ─────────────────────────────────────────────────
if date --version >/dev/null 2>&1; then
  # GNU date
  CUTOFF=$(date -u -d "${STALE_DAYS} days ago" +"%Y-%m-%dT%H:%M:%SZ")
else
  # macOS date
  CUTOFF=$(date -u -v-"${STALE_DAYS}"d +"%Y-%m-%dT%H:%M:%SZ")
fi

# ── Report setup ────────────────────────────────────────────────
REPORT_FILE=$(mktemp /tmp/stale-issue-report-XXXXXX.md)

{
  echo "# Stale Issue Cleanup Report"
  echo ""
  echo "- **Date:** $(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  echo "- **Threshold:** ${STALE_DAYS} days (cutoff: ${CUTOFF})"
  echo "- **Mode:** ${DRY_RUN:+DRY RUN}${DRY_RUN:-LIVE}"
  echo ""
} > "$REPORT_FILE"

# ── Counters ────────────────────────────────────────────────────
TOTAL_SCANNED=0
TOTAL_CLOSED=0
TOTAL_SKIPPED=0
TOTAL_PROTECTED=0

echo "============================================"
echo "  Octi Pulpo Stale Issue Cleanup"
echo "============================================"
echo "Threshold: ${STALE_DAYS} days"
echo "Cutoff:    ${CUTOFF}"
echo "Mode:      ${DRY_RUN:+DRY RUN}${DRY_RUN:-LIVE}"
echo "============================================"
echo ""

for REPO in "${REPOS[@]}"; do
  REPO_SHORT="${REPO#*/}"
  echo "--- [${REPO_SHORT}] ---"
  echo "## ${REPO_SHORT}" >> "$REPORT_FILE"
  echo "" >> "$REPORT_FILE"

  # Fetch all open issues updated before the cutoff
  # gh returns JSON; we parse with jq
  ISSUES_JSON=$(gh issue list \
    --repo "$REPO" \
    --state open \
    --json number,title,updatedAt,labels \
    --limit 200 2>/dev/null || echo "[]")

  if [ "$ISSUES_JSON" = "[]" ] || [ -z "$ISSUES_JSON" ]; then
    echo "  No open issues found"
    echo "No stale issues." >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    continue
  fi

  # Filter issues older than cutoff
  STALE_ISSUES=$(echo "$ISSUES_JSON" | jq -c --arg cutoff "$CUTOFF" \
    '[.[] | select(.updatedAt < $cutoff)]')

  STALE_COUNT=$(echo "$STALE_ISSUES" | jq 'length')

  if [ "$STALE_COUNT" -eq 0 ]; then
    echo "  No stale issues"
    echo "No stale issues." >> "$REPORT_FILE"
    echo "" >> "$REPORT_FILE"
    continue
  fi

  echo "  Found ${STALE_COUNT} stale issue(s)"

  # Build a jq filter for protected labels
  LABEL_FILTER=""
  for label in "${PROTECTED_LABELS[@]}"; do
    if [ -z "$LABEL_FILTER" ]; then
      LABEL_FILTER=".name == \"${label}\""
    else
      LABEL_FILTER="${LABEL_FILTER} or .name == \"${label}\""
    fi
  done

  # Process each stale issue (use process substitution to avoid subshell)
  while IFS= read -r issue; do
    NUMBER=$(echo "$issue" | jq -r '.number')
    TITLE=$(echo "$issue" | jq -r '.title')
    UPDATED=$(echo "$issue" | jq -r '.updatedAt')

    # Check for protected labels
    HAS_PROTECTED=$(echo "$issue" | jq \
      '[.labels[] | select('"${LABEL_FILTER}"')] | length')

    TOTAL_SCANNED=$((TOTAL_SCANNED + 1))

    if [ "$HAS_PROTECTED" -gt 0 ]; then
      PROTECTED_NAMES=$(echo "$issue" | jq -r \
        '[.labels[] | select('"${LABEL_FILTER}"') | .name] | join(", ")')
      echo "  [SKIP] #${NUMBER}: ${TITLE} (protected: ${PROTECTED_NAMES})"
      echo "- [SKIP] #${NUMBER}: ${TITLE} — protected by: \`${PROTECTED_NAMES}\`" >> "$REPORT_FILE"
      TOTAL_PROTECTED=$((TOTAL_PROTECTED + 1))
      continue
    fi

    if [ -n "$DRY_RUN" ]; then
      echo "  [DRY]  #${NUMBER}: ${TITLE} (last update: ${UPDATED})"
      echo "- [DRY]  #${NUMBER}: ${TITLE} — last activity: ${UPDATED}" >> "$REPORT_FILE"
      TOTAL_SKIPPED=$((TOTAL_SKIPPED + 1))
    else
      # Close the issue with a comment
      gh issue comment "$NUMBER" --repo "$REPO" --body "$STALE_COMMENT" 2>/dev/null
      gh issue close "$NUMBER" --repo "$REPO" 2>/dev/null

      echo "  [CLOSED] #${NUMBER}: ${TITLE}"
      echo "- [CLOSED] #${NUMBER}: ${TITLE} — last activity: ${UPDATED}" >> "$REPORT_FILE"
      TOTAL_CLOSED=$((TOTAL_CLOSED + 1))
    fi
  done < <(echo "$STALE_ISSUES" | jq -c '.[]')

  echo "" >> "$REPORT_FILE"
done

# ── Summary ─────────────────────────────────────────────────────
{
  echo ""
  echo "## Summary"
  echo ""
  echo "| Metric | Count |"
  echo "|--------|-------|"
  echo "| Repos scanned | ${#REPOS[@]} |"
  echo "| Issues scanned | ${TOTAL_SCANNED} |"
  echo "| Protected (skipped) | ${TOTAL_PROTECTED} |"
  if [ -n "$DRY_RUN" ]; then
    echo "| Would close | ${TOTAL_SKIPPED} |"
  else
    echo "| Closed | ${TOTAL_CLOSED} |"
  fi
} >> "$REPORT_FILE"

echo ""
echo "============================================"
echo "  Cleanup Summary"
echo "============================================"
echo "  Repos scanned:     ${#REPOS[@]}"
echo "  Issues scanned:    ${TOTAL_SCANNED}"
echo "  Protected:         ${TOTAL_PROTECTED}"
if [ -n "$DRY_RUN" ]; then
  echo "  Would close:       ${TOTAL_SKIPPED}"
else
  echo "  Closed:            ${TOTAL_CLOSED}"
fi
echo ""
echo "  Report: ${REPORT_FILE}"
echo "============================================"

# Print the report to stdout as well
echo ""
cat "$REPORT_FILE"
