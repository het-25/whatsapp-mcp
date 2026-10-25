# Single container for Railway: Go bridge (public port — pairing UI, REST API,
# /mcp proxy) + Python MCP server (loopback, streamable-http). See start.sh.

FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY whatsapp-bridge/go.mod whatsapp-bridge/go.sum ./
RUN go mod download
COPY whatsapp-bridge/ .
RUN CGO_ENABLED=1 go build -o /whatsapp-bridge .

FROM python:3.12-slim-bookworm
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg sqlite3 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /whatsapp-bridge /app/whatsapp-bridge
COPY whatsapp-mcp-server/ /app/mcp-server/
RUN cd /app/mcp-server && uv sync --frozen
COPY start.sh /app/start.sh
RUN chmod +x /app/start.sh
CMD ["/app/start.sh"]
