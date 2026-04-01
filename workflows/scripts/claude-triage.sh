#!/usr/bin/env bash
set -euo pipefail

# Claude API triage — classifies an issue into tier:c, tier:b-scope, or tier:a-groom
# Usage: claude-triage.sh <issue_number>
# Requires: ANTHROPIC_API_KEY, GH_TOKEN, GITHUB_REPOSITORY

ISSUE_NUMBER="${1:?Usage: claude-triage.sh <issue_number>}"
REPO="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY not set}"

# Fetch issue details
ISSUE_JSON=$(gh issue view "$ISSUE_NUMBER" --repo "$REPO" --json title,body,labels --jq '.')
TITLE=$(echo "$ISSUE_JSON" | jq -r '.title // ""')
BODY=$(echo "$ISSUE_JSON" | jq -r '.body // ""')
LABELS=$(echo "$ISSUE_JSON" | jq -r '[.labels[].name] | join(", ")' 2>/dev/null || echo "")

# Skip if already triaged
if echo "$LABELS" | grep -qE "tier:(c|b-scope|b-code|a-groom|a)"; then
  echo "SKIP: Already triaged"
  exit 0
fi

# Read repo context (README first 200 lines for project understanding)
REPO_CONTEXT=""
if [ -f "README.md" ]; then
  REPO_CONTEXT=$(head -200 README.md)
fi

# Build prompt
PROMPT_FILE=$(mktemp)
cat > "$PROMPT_FILE" <<PROMPT_EOF
You are a triage agent for a GitHub repository. Classify this issue into exactly one tier.

## Tiers

- **tier:c** — Well-scoped, implementable by Copilot coding agent. Clear what to do, single repo, has enough detail.
- **tier:b-scope** — Needs planning/scoping before implementation. Vague requirements, missing acceptance criteria, architectural decisions needed, or multi-step work that needs decomposition.
- **tier:a-groom** — Needs human architect attention. Security implications, breaking changes, cross-repo impact, budget/cost decisions, or too ambiguous for AI to scope.

## Issue

**Title:** ${TITLE}

**Body:**
${BODY}

**Existing Labels:** ${LABELS}

## Repository Context
${REPO_CONTEXT}

## Instructions

Respond with ONLY a JSON object:
{"tier": "tier:c", "reason": "one sentence explanation", "confidence": 0.85}

Choose the tier. If unsure between tier:c and tier:b-scope, choose tier:b-scope (prefer safety). If unsure between tier:b-scope and tier:a-groom, choose tier:b-scope (AI can try first).
PROMPT_EOF

# Call Anthropic Messages API
RESPONSE=$(curl -s https://api.anthropic.com/v1/messages \
  -H "x-api-key: ${ANTHROPIC_API_KEY}" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d "$(jq -n \
    --arg prompt "$(cat "$PROMPT_FILE")" \
    '{
      model: "claude-haiku-4-5-20251001",
      max_tokens: 256,
      messages: [{role: "user", content: $prompt}]
    }'
  )")

rm -f "$PROMPT_FILE"

# Parse response
CONTENT=$(echo "$RESPONSE" | jq -r '.content[0].text // ""')

if [ -z "$CONTENT" ]; then
  echo "ERROR: Empty response from Claude API"
  echo "Response: $RESPONSE"
  exit 1
fi

# Extract tier from JSON response
TIER=$(echo "$CONTENT" | jq -r '.tier // ""' 2>/dev/null || echo "")
REASON=$(echo "$CONTENT" | jq -r '.reason // "no reason provided"' 2>/dev/null || echo "parse error")
CONFIDENCE=$(echo "$CONTENT" | jq -r '.confidence // 0' 2>/dev/null || echo "0")

# Validate tier
case "$TIER" in
  tier:c|tier:b-scope|tier:a-groom)
    echo "TIER=${TIER}"
    echo "REASON=${REASON}"
    echo "CONFIDENCE=${CONFIDENCE}"
    ;;
  *)
    echo "ERROR: Invalid tier '${TIER}' — defaulting to tier:b-scope"
    TIER="tier:b-scope"
    REASON="Triage returned invalid tier, defaulting to safe option"
    CONFIDENCE="0.5"
    ;;
esac

# Apply label
gh issue edit "$ISSUE_NUMBER" --repo "$REPO" \
  --remove-label "triage:needed" \
  --add-label "$TIER" 2>/dev/null || \
gh issue edit "$ISSUE_NUMBER" --repo "$REPO" --add-label "$TIER"

# Post triage comment
TIER_EMOJI=""
TIER_DESC=""
case "$TIER" in
  tier:c)
    TIER_EMOJI="🤖"
    TIER_DESC="**Tier C — Copilot Implementation.** Issue is well-scoped and ready for automated coding."
    ;;
  tier:b-scope)
    TIER_EMOJI="🧠"
    TIER_DESC="**Tier B — Needs Planning.** Issue requires scoping and decomposition before implementation."
    ;;
  tier:a-groom)
    TIER_EMOJI="👤"
    TIER_DESC="**Tier A — Human Grooming Required.** Issue needs architect attention before proceeding."
    ;;
esac

gh issue comment "$ISSUE_NUMBER" --repo "$REPO" --body "${TIER_EMOJI} **Triage complete**

${TIER_DESC}

**Reason:** ${REASON}
**Confidence:** ${CONFIDENCE}

_Powered by Octi Pulpo pipeline_"

# Output for workflow consumption
echo "tier=${TIER}" >> "$GITHUB_OUTPUT"
echo "reason=${REASON}" >> "$GITHUB_OUTPUT"
echo "confidence=${CONFIDENCE}" >> "$GITHUB_OUTPUT"
