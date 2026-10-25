#!/bin/bash
# Mint / list / revoke scoped WhatsApp-bridge access tokens, and print the
# ready-to-run `claude mcp add` command for a new agent connection.
#
# Every token can always READ all chats. This controls SEND access only.
#
# Usage:
#   wa-grant.sh grant <label> all                    # send to any chat
#   wa-grant.sh grant <label> none                   # read-only (no sending)
#   wa-grant.sh grant <label> 919876543210,<jid>...  # send only to these chats
#   wa-grant.sh list                                 # show existing tokens
#   wa-grant.sh revoke <label-or-token>              # remove a token
#
# Reads WA_BRIDGE_URL and WA_ADMIN_TOKEN from the environment, falling back to
# the .railway-secrets file written by deploy-railway.sh.

set -euo pipefail
SECRETS="$(dirname "$0")/.railway-secrets"
[ -f "$SECRETS" ] && { WA_BRIDGE_URL="${WA_BRIDGE_URL:-$(grep '^WA_BRIDGE_URL=' "$SECRETS" | cut -d= -f2-)}"; WA_ADMIN_TOKEN="${WA_ADMIN_TOKEN:-$(grep '^WA_ADMIN_TOKEN=' "$SECRETS" | cut -d= -f2-)}"; }
BASE="${WA_BRIDGE_URL:?set WA_BRIDGE_URL (e.g. https://<app>.up.railway.app)}"
ADMIN_TOKEN="${WA_ADMIN_TOKEN:?set WA_ADMIN_TOKEN (the bridge admin token)}"
AUTH="Authorization: Bearer $ADMIN_TOKEN"

cmd="${1:-}"; shift || true

# Until a phone is linked, every route redirects to the pairing page.
if [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/tokens" -H "$AUTH")" = "302" ]; then
  echo "Bridge isn't paired yet — open $BASE/pair and scan the QR first."; exit 1
fi

case "$cmd" in
  grant)
    label="${1:?label required}"; scope="${2:?scope required: all|none|<csv of chats>}"
    case "$scope" in
      all|none) send="\"$scope\"" ;;
      *) # comma-separated chats -> JSON array
         IFS=',' read -ra parts <<< "$scope"
         arr=""; for p in "${parts[@]}"; do p="$(echo "$p" | xargs)"; [ -n "$p" ] && arr="$arr\"$p\","; done
         send="[${arr%,}]" ;;
    esac
    resp="$(curl -s -X POST "$BASE/api/tokens" -H "$AUTH" -H "Content-Type: application/json" \
      -d "{\"label\":\"$label\",\"send\":$send}")"
    token="$(echo "$resp" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))' 2>/dev/null || true)"
    if [ -z "$token" ]; then echo "Failed: $resp"; exit 1; fi
    echo "✅ Token minted for '$label' (send=$scope)"
    echo
    echo "Add this agent's WhatsApp access with:"
    echo "  claude mcp add --scope user --transport http whatsapp-$label \\"
    echo "    \"$BASE/mcp\" --header \"Authorization: Bearer $token\""
    echo
    echo "Raw token (store securely): $token"
    ;;
  list)
    curl -s "$BASE/api/tokens" -H "$AUTH" | python3 -m json.tool ;;
  revoke)
    id="${1:?label-or-token required}"
    curl -s -X DELETE "$BASE/api/tokens?id=$id" -H "$AUTH" | python3 -m json.tool ;;
  *)
    grep '^#' "$0" | sed 's/^# \{0,1\}//' | tail -n +2 ; exit 1 ;;
esac
