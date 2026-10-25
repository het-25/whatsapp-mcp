#!/bin/bash
# One-command deploy of the bridge + MCP server to Railway.
#
#   ./deploy-railway.sh [project-name]      # first deploy (creates everything)
#   railway up -y -d                        # later redeploys, from this directory
#
# Creates a Railway project with one service, a volume at /app/store (WhatsApp
# session + message DBs survive restarts), a public domain and random secrets,
# then deploys this directory. Prints the pairing link and the `claude mcp add`
# line. Secrets are written to ./.railway-secrets (gitignored, mode 600).
#
# Needs: railway CLI (`brew install railway` / `npm i -g @railway/cli`), openssl, python3.

set -euo pipefail
cd "$(dirname "$0")"
NAME="${1:-whatsapp-bridge}"
SECRETS=.railway-secrets

command -v railway >/dev/null || { echo "Install the Railway CLI first: brew install railway"; exit 1; }
railway whoami >/dev/null 2>&1 || railway login

if [ -f "$SECRETS" ]; then
  echo "$SECRETS exists — already deployed from here. Redeploy with: railway up -y -d"; exit 1
fi

# Railway's CLI echoes variable values; keep its chatter (and our secrets) off
# the terminal unless a step fails.
quiet() { local out; out="$("$@" 2>&1)" || { echo "$out" >&2; echo "✗ failed: $1 $2" >&2; exit 1; }; }

TOKEN="$(openssl rand -hex 32)"
PAIR_PASS="$(openssl rand -hex 16)"

echo "→ Creating project '$NAME'"
quiet railway init --name "$NAME"
quiet railway add --service "$NAME" \
  --variables "PORT=8080" \
  --variables "WHATSAPP_BRIDGE_BIND=0.0.0.0" \
  --variables "WHATSAPP_BRIDGE_TOKEN=$TOKEN" \
  --variables "PAIR_UI_USER=admin" \
  --variables "PAIR_UI_PASS=$PAIR_PASS" \
  --variables "WHATSAPP_DEVICE_NAME=Mac OS" \
  --variables "WHATSAPP_PLATFORM_TYPE=chrome"
quiet railway service link "$NAME"

echo "→ Adding volume and domain"
# `volume add` wants the service ID and panics in its interactive path, so use --json.
SERVICE_ID="$(railway status --json | python3 -c 'import json,sys; d=json.load(sys.stdin); print(next(e["node"]["id"] for e in d["services"]["edges"] if e["node"]["name"]==sys.argv[1]))' "$NAME")"
quiet railway volume --service "$SERVICE_ID" add --mount-path /app/store --json
DOMAIN="$(railway domain --service "$NAME" --port 8080 --json | grep -oE '[a-z0-9-]+\.up\.railway\.app' | head -1)"
[ -n "$DOMAIN" ] || { echo "Could not create a domain; run: railway domain --port 8080"; exit 1; }
URL="https://$DOMAIN"
quiet railway variable set --service "$NAME" --skip-deploys "WHATSAPP_BRIDGE_ALLOWED_HOSTS=$DOMAIN"

umask 077
cat > "$SECRETS" <<EOF
WA_BRIDGE_URL=$URL
WA_ADMIN_TOKEN=$TOKEN
PAIR_UI_PASS=$PAIR_PASS
EOF

echo "→ Deploying (first build takes ~3–5 min)"
railway up --service "$NAME" -y -d

cat <<EOF

✅ Deployed. Once the build is live (railway logs to watch):

1. Pair your phone — open this link and scan the QR from
   WhatsApp → Settings → Linked devices → Link a device:
     $URL/pair?key=$PAIR_PASS

2. Connect Claude Code (admin token: can read everything and send anywhere):
     claude mcp add --scope user --transport http whatsapp "$URL/mcp" \\
       --header "Authorization: Bearer $TOKEN"
   For a read-only or limited-send token instead: ./wa-grant.sh grant laptop none

Secrets saved in $SECRETS — keep that file private.
EOF
