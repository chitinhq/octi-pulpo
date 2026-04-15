#!/usr/bin/env bash
# octi-pulpo-mcp.sh — wrapper that ensures GITHUB_TOKEN (and related env) is
# populated before exec'ing the octi-pulpo binary as an MCP server.
#
# Claude Code spawns MCP servers in a stripped environment — it inherits only
# what the parent shell exports AND what the server's `.mcp.json` env block
# declares. Interactive shells typically rely on `gh auth` or a gitignored
# env file for GITHUB_TOKEN, which the spawn environment doesn't see. Without
# GITHUB_TOKEN, bootcheck's `adapter_reachability` probe stays YELLOW.
#
# This wrapper resolves the token from the first available source:
#   1. $GITHUB_TOKEN (already set — honored as-is)
#   2. $COPILOT_PAT  (legacy name, same scope)
#   3. ~/.config/octi/env (gitignored KEY=VALUE file the user maintains)
#   4. `gh auth token` (if the gh CLI is authenticated)
#   5. ~/.config/gh/hosts.yml oauth_token (fallback for old gh versions)
#
# Format of ~/.config/octi/env (create it yourself — never commit):
#   GITHUB_TOKEN=ghp_xxx
#   # optional extras:
#   # ANTHROPIC_API_KEY=sk-ant-xxx
#
# Permissions: chmod 600 ~/.config/octi/env

set -euo pipefail

# 3) gitignored env file
ENV_FILE="${OCTI_ENV_FILE:-${HOME}/.config/octi/env}"
if [ -z "${GITHUB_TOKEN:-}" ] && [ -f "${ENV_FILE}" ]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
fi

# 2) COPILOT_PAT legacy alias
if [ -z "${GITHUB_TOKEN:-}" ] && [ -n "${COPILOT_PAT:-}" ]; then
  export GITHUB_TOKEN="${COPILOT_PAT}"
fi

# 4) gh CLI (modern)
if [ -z "${GITHUB_TOKEN:-}" ] && command -v gh >/dev/null 2>&1; then
  if tok="$(gh auth token 2>/dev/null)" && [ -n "${tok}" ]; then
    export GITHUB_TOKEN="${tok}"
  fi
fi

# 5) gh hosts.yml fallback (pre-2.x gh lacks `auth token`)
if [ -z "${GITHUB_TOKEN:-}" ]; then
  HOSTS_YML="${HOME}/.config/gh/hosts.yml"
  if [ -f "${HOSTS_YML}" ]; then
    tok="$(awk '/oauth_token:/ {print $2; exit}' "${HOSTS_YML}" 2>/dev/null || true)"
    if [ -n "${tok}" ]; then
      export GITHUB_TOKEN="${tok}"
    fi
  fi
fi

# Default MCP env (mirrors wire-mcp.sh)
export OCTI_REDIS_URL="${OCTI_REDIS_URL:-redis://localhost:6379}"
export OCTI_NAMESPACE="${OCTI_NAMESPACE:-octi}"

BINARY="${OCTI_PULPO_BIN:-${HOME}/.chitin/bin/octi-pulpo}"
if [ ! -x "${BINARY}" ]; then
  # Fallback to workspace build path
  ALT="${HOME}/workspace/octi/bin/octi-pulpo"
  if [ -x "${ALT}" ]; then
    BINARY="${ALT}"
  else
    echo "ERROR: octi-pulpo binary not found at ${BINARY} or ${ALT}" >&2
    exit 1
  fi
fi

exec "${BINARY}" "$@"
