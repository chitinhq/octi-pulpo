#!/usr/bin/env bash
# wire-mcp.sh — register octi-pulpo as an MCP server in the workspace Claude settings.
#
# Usage:
#   bash scripts/wire-mcp.sh
#   make wire-mcp          # builds, installs, then wires
#
# Environment:
#   CHITIN_WORKSPACE       path to the workspace (default: ~/workspace)
#   INSTALL_DIR            where the binary lives (default: ~/.chitin/bin)

set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-${HOME}/.chitin/bin}"
BINARY="${INSTALL_DIR}/octi-pulpo"
WORKSPACE="${CHITIN_WORKSPACE:-${HOME}/workspace}"
SETTINGS="${WORKSPACE}/.claude/settings.json"
# Wrapper resolves GITHUB_TOKEN from gh/env-file so MCP spawns can reach GH.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WRAPPER="${SCRIPT_DIR}/octi-pulpo-mcp.sh"

if [ ! -f "${BINARY}" ]; then
  echo "ERROR: octi-pulpo binary not found at ${BINARY}" >&2
  echo "Run 'make install' or 'make wire-mcp' first." >&2
  exit 1
fi

if [ ! -x "${WRAPPER}" ]; then
  echo "ERROR: MCP wrapper not found or not executable: ${WRAPPER}" >&2
  exit 1
fi

if [ ! -f "${SETTINGS}" ]; then
  echo "ERROR: Claude settings not found at ${SETTINGS}" >&2
  exit 1
fi

if ! command -v jq &>/dev/null; then
  echo "ERROR: jq is required to update settings. Install with: sudo apt install jq" >&2
  exit 1
fi

# Build the mcpServers entry. The wrapper script resolves GITHUB_TOKEN from
# the user's gh auth / ~/.config/octi/env so the GH adapter can actually probe
# (bootcheck adapter_reachability flips YELLOW → GREEN).
MCP_ENTRY=$(cat <<EOF
{
  "command": "${WRAPPER}",
  "env": {
    "OCTI_REDIS_URL": "redis://localhost:6379",
    "OCTI_NAMESPACE": "octi",
    "OCTI_PULPO_BIN": "${BINARY}"
  }
}
EOF
)

TMP=$(mktemp)
jq --argjson entry "${MCP_ENTRY}" '.mcpServers["octi-pulpo"] = $entry' "${SETTINGS}" > "${TMP}"
mv "${TMP}" "${SETTINGS}"

echo "octi-pulpo registered as MCP server in ${SETTINGS}"
echo "Binary: ${BINARY}"
echo ""
echo "Restart Claude Code to pick up the new MCP server."
