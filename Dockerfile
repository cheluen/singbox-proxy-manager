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
    && DIGEST="$(curl -fsSL "https://api.github.com/repos/SagerNet/sing-box/releases/tags/v${SINGBOX_VERSION}" | jq -r --arg asset "${ASSET}" '.assets[] | select(.name==$asset) | .digest')" \
    && test -n "$DIGEST" && test "$DIGEST" != "null" \
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
