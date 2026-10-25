#!/bin/sh
# Container entrypoint: run the MCP server (loopback) beside the bridge
# (public). The bridge reverse-proxies /mcp to it — one port serves all.
set -u
BRIDGE_PORT="${PORT:-8080}"
export WHATSAPP_BRIDGE_PORT="$BRIDGE_PORT"
export WHATSAPP_API_URL="http://127.0.0.1:${BRIDGE_PORT}/api"
export WHATSAPP_DB_PATH=/app/store/messages.db
export WHATSMEOW_DB_PATH=/app/store/whatsapp.db
export WHATSAPP_MCP_TRANSPORT=http
export WHATSAPP_MCP_HOST=127.0.0.1
export WHATSAPP_MCP_PORT=9090
export WHATSAPP_MCP_PROXY=http://127.0.0.1:9090

# WHATSAPP_RESET_SESSION=1 for one boot wipes the session to force a fresh QR
# pair. Remove it afterwards, or every restart unpairs again.
if [ "${WHATSAPP_RESET_SESSION:-}" = "1" ]; then
    echo "WHATSAPP_RESET_SESSION=1: wiping stored session" >&2
    rm -f /app/store/whatsapp.db /app/store/messages.db /app/store/.bridge-token
fi

(cd /app/mcp-server && while true; do
    uv run --frozen main.py
    echo "mcp server exited; restarting in 3s" >&2
    sleep 3
done) &

exec /app/whatsapp-bridge -full-history-pair
