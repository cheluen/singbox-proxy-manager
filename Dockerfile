FROM node:18-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
RUN npm run build

FROM golang:1.21 AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
# Force rebuild by adding timestamp
RUN echo "Build at $(date)" > /tmp/buildtime
COPY backend/ ./backend/
RUN CGO_ENABLED=1 go build -o main ./backend

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && curl -Lo /tmp/sing-box.tar.gz https://github.com/SagerNet/sing-box/releases/download/v1.12.11/sing-box-1.12.11-linux-amd64.tar.gz \
    && tar -xzf /tmp/sing-box.tar.gz -C /tmp \
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
ENV ADMIN_PASSWORD=admin123

EXPOSE 30000
VOLUME ["/app/config"]
CMD ["./main"]
