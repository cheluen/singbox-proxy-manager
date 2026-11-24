FROM node:22-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.26.11 AS backend-builder
ENV GOTOOLCHAIN=local
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY scripts/ ./scripts/
COPY backend/ ./backend/
COPY frontend/ ./frontend/
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
COPY internal/ ./internal/
RUN ./scripts/build-release.sh /app/singbox-proxy-manager

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=backend-builder /app/singbox-proxy-manager ./singbox-proxy-manager
COPY LICENSE THIRD_PARTY_NOTICES.md USER_AGREEMENT.md /app/
COPY licenses/SING-BOX-GPL-3.0.txt /app/licenses/SING-BOX-GPL-3.0.txt
RUN mkdir -p /app/config

ENV PORT=30000
ENV BIND_ADDRESS=0.0.0.0
ENV CONFIG_DIR=/app/config
ENV TZ=UTC+8
ENV ACCEPT_USER_AGREEMENT=false
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
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["/app/singbox-proxy-manager", "--healthcheck"]
CMD ["./singbox-proxy-manager"]
