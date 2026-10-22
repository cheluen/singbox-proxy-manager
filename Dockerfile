FROM node:18-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.26.10 AS backend-builder
ARG SINGBOX_VERSION=1.12.12
ENV GOTOOLCHAIN=local
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY scripts/ ./scripts/
RUN ./scripts/build-sing-box.sh /usr/local/bin/sing-box
# Force rebuild by adding timestamp
RUN echo "Build at $(date)" > /tmp/buildtime
COPY backend/ ./backend/
COPY frontend/ ./frontend/
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -tags frontend_dist -trimpath -o singbox-proxy-manager ./backend \
    && ./scripts/verify-go-build.sh /app/singbox-proxy-manager

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl jq \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=backend-builder /app/singbox-proxy-manager ./singbox-proxy-manager
COPY --from=backend-builder /usr/local/bin/sing-box /usr/local/bin/sing-box
RUN mkdir -p /app/config

ENV PORT=30000
ENV BIND_ADDRESS=0.0.0.0
ENV CONFIG_DIR=/app/config
ENV TZ=UTC+8
ENV ADMIN_PASSWORD=
ENV SINGBOX_ENV_ALLOWLIST=
ENV SBPM_SINGBOX_LOG_OUTPUT=stdout
ENV SBPM_SINGBOX_LOG_MAX_BYTES=10485760
ENV SBPM_SINGBOX_LOG_BACKUPS=3
ENV DATABASE_URL=
ENV POSTGRES_DATABASE_URL=
ENV MYSQL_DATABASE_URL=
ENV PGSQL=
ENV MYSQL=
ENV MYSQL_DATABASE_NAME=
ENV TURSO_DATABASE_URL=
ENV TURSO_AUTH_TOKEN=
ENV SBPM_UPDATE_CHECK_DISABLED=false
ENV SBPM_UPDATE_CHECK_REPO=cheluen/singbox-proxy-manager
ENV SBPM_UPDATE_CHECK_TTL=6h
ENV SBPM_UPDATE_CHECK_TIMEOUT=5s

EXPOSE 30000
VOLUME ["/app/config"]
CMD ["./singbox-proxy-manager"]
