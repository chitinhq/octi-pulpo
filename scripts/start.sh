#!/bin/bash
# Start the full Octi Pulpo stack:
#   1. octi-pulpo daemon (webhook server + MCP)
#   2. octi-worker (Redis-native workers)
#   3. octi-timer (cron replacement)
#
# Replaces the old deploy.sh + crontab + worker.sh approach with three Go binaries.
set -euo pipefail

WORKSPACE="${WORKSPACE_DIR:-$HOME/agentguard-workspace}"
REPO="$WORKSPACE/octi-pulpo"
BIN="$REPO/bin"
LOGDIR="$WORKSPACE/server/logs"
REDIS_URL="${OCTI_REDIS_URL:-redis://localhost:6379}"
NAMESPACE="${OCTI_NAMESPACE:-agentguard-workspace}"
WORKERS="${OCTI_WORKERS:-32}"
HTTP_PORT="${OCTI_HTTP_PORT:-8787}"

mkdir -p "$LOGDIR" "$BIN"

# Build all binaries
echo "Building octi-pulpo stack..."
cd "$REPO"
go build -o bin/octi-pulpo ./cmd/octi-pulpo/
go build -o bin/octi-worker ./cmd/octi-worker/
go build -o bin/octi-timer ./cmd/octi-timer/
echo "Build complete."

# Kill existing instances (if any)
for proc in octi-pulpo octi-worker octi-timer; do
    pkill -f "$BIN/$proc" 2>/dev/null || true
done
sleep 1

# Start dispatcher daemon
OCTI_HTTP_PORT="$HTTP_PORT" \
OCTI_DAEMON=1 \
OCTI_REDIS_URL="$REDIS_URL" \
OCTI_NAMESPACE="$NAMESPACE" \
AGENTGUARD_HEALTH_DIR="$HOME/.agentguard/driver-health" \
AGENTGUARD_WEBHOOK_SECRET_FILE="$HOME/.agentguard/webhook-secret" \
nohup "$BIN/octi-pulpo" > "$LOGDIR/octi-pulpo.log" 2>&1 &
PULPO_PID=$!

# Wait briefly for dispatcher to be ready
sleep 2

# Start workers
OCTI_REDIS_URL="$REDIS_URL" \
OCTI_NAMESPACE="$NAMESPACE" \
OCTI_WORKERS="$WORKERS" \
WORKSPACE_DIR="$WORKSPACE" \
nohup "$BIN/octi-worker" > "$LOGDIR/octi-worker.log" 2>&1 &
WORKER_PID=$!

# Start timer (replaces crontab)
OCTI_SCHEDULE="$WORKSPACE/server/schedule.json" \
OCTI_DISPATCHER_URL="http://localhost:$HTTP_PORT" \
nohup "$BIN/octi-timer" > "$LOGDIR/octi-timer.log" 2>&1 &
TIMER_PID=$!

echo ""
echo "Octi Pulpo stack started"
echo "  Dispatcher: http://localhost:$HTTP_PORT (PID $PULPO_PID)"
echo "  Workers:    $WORKERS goroutines (PID $WORKER_PID)"
echo "  Timer:      reading schedule.json (PID $TIMER_PID)"
echo ""
echo "Logs:"
echo "  $LOGDIR/octi-pulpo.log"
echo "  $LOGDIR/octi-worker.log"
echo "  $LOGDIR/octi-timer.log"
echo ""
echo "Stop: kill $PULPO_PID $WORKER_PID $TIMER_PID"
