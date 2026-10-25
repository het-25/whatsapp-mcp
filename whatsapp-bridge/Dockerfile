FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /whatsapp-bridge .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates sqlite3 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /whatsapp-bridge /app/whatsapp-bridge
# Railway injects PORT; the bridge reads WHATSAPP_BRIDGE_PORT. Session + messages live in /app/store (mount a volume there).
# Set WHATSAPP_RESET_SESSION=1 for one boot to wipe the stored session and force a fresh QR pair (e.g. to change the linked-device label). Remove it afterwards.
CMD ["/bin/sh", "-c", "if [ \"$WHATSAPP_RESET_SESSION\" = \"1\" ]; then echo 'WHATSAPP_RESET_SESSION=1: wiping stored session'; rm -f /app/store/whatsapp.db /app/store/messages.db /app/store/.bridge-token; fi; WHATSAPP_BRIDGE_PORT=${PORT:-8080} exec /app/whatsapp-bridge -full-history-pair"]
