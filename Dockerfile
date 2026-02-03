FROM node:18-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
RUN npm run build

FROM golang:1.24 AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
# Force rebuild by adding timestamp
RUN echo "Build at $(date)" > /tmp/buildtime
COPY backend/ ./backend/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -o main ./backend

FROM debian:bookworm-slim
ARG SINGBOX_VERSION=1.12.12
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    jq \
    && ASSET="sing-box-${SINGBOX_VERSION}-linux-amd64.tar.gz" \
    && curl -fL -o "/tmp/${ASSET}" "https://github.com/SagerNet/sing-box/releases/download/v${SINGBOX_VERSION}/${ASSET}" \
    && GH_API_URL="https://api.github.com/repos/SagerNet/sing-box/releases/tags/v${SINGBOX_VERSION}" \
    && GH_BODY="$(mktemp)" \
    && GH_HEADERS="$(mktemp)" \
    && GH_STATUS="$(curl -sS -D "$GH_HEADERS" -o "$GH_BODY" -w "%{http_code}" "$GH_API_URL" || true)" \
    && if [ "$GH_STATUS" != "200" ]; then \
        echo "ERROR: failed to fetch sing-box release metadata from GitHub API (api.github.com)." >&2; \
        echo "URL: $GH_API_URL" >&2; \
        echo "HTTP status: $GH_STATUS" >&2; \
        echo "Hint: your build environment may be blocking api.github.com or hitting GitHub API rate limits." >&2; \
        echo "GitHub rate limit headers:" >&2; \
        grep -i '^x-ratelimit' "$GH_HEADERS" >&2 || true; \
        echo "Response body (first 2000 bytes):" >&2; \
        head -c 2000 "$GH_BODY" >&2 || true; \
        echo >&2; \
        exit 1; \
      fi \
    && DIGEST="$(jq -r --arg asset "${ASSET}" '.assets[] | select(.name==$asset) | .digest' "$GH_BODY")" \
    && rm -f "$GH_BODY" "$GH_HEADERS" \
    && if [ -z "$DIGEST" ] || [ "$DIGEST" = "null" ]; then \
        echo "ERROR: failed to find digest for asset '$ASSET' in GitHub release metadata." >&2; \
        exit 1; \
      fi \
    && DIGEST="${DIGEST#sha256:}" \
    && echo "${DIGEST}  /tmp/${ASSET}" | sha256sum -c - \
    && tar -xzf "/tmp/${ASSET}" -C /tmp \
    && mv /tmp/sing-box-*/sing-box /usr/local/bin/ \
    && chmod +x /usr/local/bin/sing-box \
    && rm -rf /tmp/sing-box* \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=backend-builder /app/main .
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
RUN mkdir -p /app/config

ENV PORT=30000
ENV CONFIG_DIR=/app/config
ENV ADMIN_PASSWORD=

EXPOSE 30000
VOLUME ["/app/config"]
CMD ["./main"]
