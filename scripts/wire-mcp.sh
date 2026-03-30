#!/usr/bin/env bash
# wire-mcp.sh — register octi-pulpo as an MCP server in the workspace Claude settings.
#
# Usage:
#   bash scripts/wire-mcp.sh
#   make wire-mcp          # builds, installs, then wires
#
# Environment:
#   AGENTGUARD_WORKSPACE   path to the workspace (default: ~/agentguard-workspace)
#   INSTALL_DIR            where the binary lives (default: ~/.agentguard/bin)

set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-${HOME}/.agentguard/bin}"
BINARY="${INSTALL_DIR}/octi-pulpo"
WORKSPACE="${AGENTGUARD_WORKSPACE:-${HOME}/agentguard-workspace}"
SETTINGS="${WORKSPACE}/.claude/settings.json"

if [ ! -f "${BINARY}" ]; then
  echo "ERROR: octi-pulpo binary not found at ${BINARY}" >&2
  echo "Run 'make install' or 'make wire-mcp' first." >&2
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

# Build the mcpServers entry.
MCP_ENTRY=$(cat <<EOF
{
  "command": "${BINARY}",
  "env": {
    "OCTI_REDIS_URL": "redis://localhost:6379",
    "OCTI_NAMESPACE": "octi"
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
