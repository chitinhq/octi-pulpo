#!/usr/bin/env bash
# tunnel-webhook-sync.sh — watches cloudflared quick tunnel URL and syncs
# GitHub webhook endpoints across all repos when it changes.
#
# Runs as a systemd service alongside octi-tunnel.service.
# Reads tunnel URL from journalctl, compares to last known URL in Redis,
# and updates all GitHub webhooks if changed.

set -euo pipefail

SECRET_FILE="${CHITIN_WEBHOOK_SECRET_FILE:-$HOME/.chitin/webhook-secret}"
REDIS_KEY="octi:tunnel-url"
HOOK_IDS_FILE="$HOME/.chitin/webhook-hooks.txt"
CHECK_INTERVAL=30  # seconds between checks

log() { echo "[tunnel-sync] $(date -u +%H:%M:%S) $*" >&2; }

get_tunnel_url() {
  journalctl --user -u octi-tunnel.service --no-pager -n 50 2>/dev/null \
    | grep -oP 'https://[a-z0-9\-]+\.trycloudflare\.com' \
    | tail -1
}

get_stored_url() {
  docker exec redis-octi redis-cli GET "$REDIS_KEY" 2>/dev/null | tr -d '"' || true
}

store_url() {
  docker exec redis-octi redis-cli SET "$REDIS_KEY" "$1" >/dev/null 2>&1
}

update_webhooks() {
  local webhook_url="$1/webhook"
  local secret
  secret=$(cat "$SECRET_FILE" 2>/dev/null || echo "")

  if [[ -z "$secret" ]]; then
    log "ERROR: no webhook secret at $SECRET_FILE"
    return 1
  fi

  if [[ ! -f "$HOOK_IDS_FILE" ]]; then
    log "ERROR: no hook IDs file at $HOOK_IDS_FILE"
    return 1
  fi

  local updated=0
  local failed=0

  while IFS=': ' read -r repo hookid; do
    [[ "$repo" == \#* ]] && continue
    [[ -z "$hookid" || -z "$repo" ]] && continue

    payload=$(jq -n --arg url "$webhook_url" --arg secret "$secret" \
      '{"config":{"url":$url,"content_type":"json","secret":$secret}}')
    if echo "$payload" | gh api "repos/$repo/hooks/$hookid" --method PATCH --input - >/dev/null 2>&1; then
      updated=$((updated + 1))
    else
      log "WARN: failed to update $repo hook $hookid"
      failed=$((failed + 1))
    fi
  done < "$HOOK_IDS_FILE"

  log "Updated $updated webhooks ($failed failed) → $webhook_url"

  # Update the stored URL reference in the hooks file
  sed -i "s|^# URL:.*|# URL: $webhook_url|" "$HOOK_IDS_FILE" 2>/dev/null || true
}

# ── Main loop ──
log "Starting tunnel webhook sync (check every ${CHECK_INTERVAL}s)"

while true; do
  current_url=$(get_tunnel_url)

  if [[ -z "$current_url" ]]; then
    sleep "$CHECK_INTERVAL"
    continue
  fi

  stored_url=$(get_stored_url)

  if [[ "$current_url" != "$stored_url" ]]; then
    log "Tunnel URL changed: ${stored_url:-<none>} → $current_url"
    update_webhooks "$current_url"
    store_url "$current_url"

    # Verify health through the tunnel
    if curl -sf "$current_url/health" >/dev/null 2>&1; then
      log "Health check passed through tunnel"
    else
      log "WARN: health check failed through tunnel"
    fi
  fi

  sleep "$CHECK_INTERVAL"
done
