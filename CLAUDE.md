# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

SingBox Proxy Manager is a web-based management system for sing-box proxy nodes. It uses a **single-process architecture** where one sing-box process manages all proxy nodes simultaneously through a unified configuration file.

**Tech Stack:**
- Backend: Go 1.21 with Gin framework
- Database: SQLite
- Frontend: React 18 + Ant Design 5 + Vite 5
- Proxy Engine: sing-box 1.12.11
- Deployment: Docker with host networking

## Development Commands

### Backend (Go)

```bash
# Build backend
cd /root/project/singbox-proxy-manager-main
CGO_ENABLED=1 go build -o main ./backend

# Run backend directly (requires sing-box installed)
./main

# Run with custom config directory
CONFIG_DIR=/custom/path PORT=30000 ADMIN_PASSWORD=admin123 ./main

# Download dependencies
go mod download

# Update dependencies
go mod tidy
```

### Frontend (React)

```bash
cd /root/project/singbox-proxy-manager-main/frontend

# Install dependencies
npm install

# Development server (port 5173 by default)
npm run dev

# Build for production
npm run build

# Preview production build
npm run preview
```

### Docker

```bash
# Build and start
docker compose up -d --build

# View logs
docker compose logs -f

# Restart service
docker compose restart

# Stop service
docker compose down

# Rebuild without cache
docker compose build --no-cache
docker compose up -d
```

### Testing Proxy Connectivity

```bash
# Test HTTP proxy with authentication
curl --proxy http://username:password@localhost:30001 http://httpbin.org/ip

# Test SOCKS5 proxy
curl --proxy socks5://username:password@localhost:30001 http://httpbin.org/ip

# Check sing-box process
docker exec sb-proxy ps aux | grep sing-box

# View sing-box logs
docker exec sb-proxy tail -f /app/config/singbox.log

# Check listening ports
docker exec sb-proxy ss -tlnp
```

## Architecture

### Single-Process Design

The system uses **one sing-box process** for all nodes, not one process per node. This is critical to understand:

- `backend/services/singbox.go`: Manages the single sing-box process
- `GenerateGlobalConfig()`: Creates unified config with all nodes' inbounds/outbounds
- When nodes change, the entire config is regenerated and sing-box restarts

### Configuration Flow

1. **User adds/updates node** → Handler receives request
2. **Database updated** → Node stored in SQLite (`proxy_nodes` table)
3. **Config regeneration** → `GenerateGlobalConfig()` reads all nodes from DB
4. **Unified config** → Single `config.json` with multiple inbounds + outbounds
5. **Restart sing-box** → Process restarted to apply new config

### Data Flow

```
Frontend (React)
  ↓ HTTP API
Backend (Gin handlers in api/handlers.go)
  ↓ SQL queries
Database (SQLite: proxy.db)
  ↓ GenerateGlobalConfig()
Sing-box Config (config.json with all nodes)
  ↓ Process restart
Sing-box (single process, multiple ports)
```

### Key Components

**Backend (`backend/`):**
- `main.go`: Application entry point, initializes DB and sing-box, starts Gin server
- `models/proxy.go`: Database models for proxy nodes (SS, VLESS, VMess, Hysteria2, TUIC)
- `api/handlers.go`: HTTP API handlers for CRUD operations on nodes
- `services/singbox.go`: Sing-box process management and config generation
- `services/sharelink.go`: Share link parsers (ss://, vless://, vmess://, etc.)
- `services/ipcheck.go`: IP detection for proxy nodes

**Frontend (`frontend/src/`):**
- `App.jsx`: Main application component with routing
- `components/`: React components for UI (node list, forms, etc.)
- `i18n/`: Internationalization (Chinese/English)
- `utils/api.js`: Axios API client

**Configuration:**
- `config/config.json`: Generated sing-box configuration (unified for all nodes)
- `config/proxy.db`: SQLite database with node data
- `config/singbox.log`: Sing-box process logs

### Port Allocation

- Port `30000`: Web management interface (configurable via `PORT` env var)
- Ports `30001+`: Proxy inbound ports (auto-assigned or manually set)
- Each node gets a unique inbound port with "mixed" type (HTTP + SOCKS5)
- Nodes can be reordered via drag-and-drop, which reassigns ports sequentially

### Protocol Support

The system supports these outbound protocols via sing-box:
- **Shadowsocks** (`ss://`): AEAD encryption
- **VLESS** (`vless://`): Supports Reality, WebSocket, gRPC, HTTPUpgrade
- **VMess** (`vmess://`): Supports WebSocket, HTTP/2, gRPC
- **Hysteria2** (`hy2://`, `hysteria2://`): UDP-based with brutal/salamander obfuscation
- **TUIC** (`tuic://`): QUIC-based protocol

Each node gets a "mixed" inbound (HTTP + SOCKS5) on a unique port, routing to its specific outbound.

## Important Implementation Details

### Authentication System

- Login uses bcrypt password hashing (see `api/handlers.go:Login`)
- Session managed via in-memory token with expiry time
- Default admin password is `admin123` (set via `ADMIN_PASSWORD` env var or DB)
- Each proxy node can have optional HTTP/SOCKS5 auth (username/password)

### Configuration Generation

`backend/services/singbox.go` contains the core logic:

- `GenerateGlobalConfig()`: Creates a single config with all enabled nodes
- Each node gets:
  - **Inbound**: `mixed` type with tag `node-{id}-in` on unique port
  - **Outbound**: Protocol-specific with tag `node-{id}-out`
  - **Route rule**: Direct mapping from inbound to outbound
- Uses `Extra` fields pattern to handle dynamic sing-box config properties
- Custom marshaling merges `Extra` maps into final JSON

### Share Link Parsing

`backend/services/sharelink.go` parses subscription links:

- Each protocol has a dedicated parser function
- Handles base64 encoding, URL parameters, fragment names
- Returns protocol-specific config struct + node name + type
- Used for both single node addition and batch import

### Node Operations

Critical operations that trigger config regeneration:
- Create node
- Update node (enable/disable, change auth, change port)
- Delete node
- Reorder nodes (changes port assignments)
- Batch import
- Batch set authentication

All these call `regenerateAndRestart()` in `api/handlers.go`.

### Database Schema

`proxy_nodes` table:
- Core fields: `id`, `name`, `type`, `config` (JSON), `inbound_port`, `enabled`
- Auth fields: `username`, `password`
- IP detection: `node_ip`, `location`, `country_code`, `latency`
- Ordering: `sort_order` (used for drag-and-drop)

`settings` table:
- `admin_password`: Hashed admin password
- `start_port`: Default starting port for auto-assignment

### Environment Variables

Required for Docker deployment:
- `PORT`: Web interface port (default: 30000)
- `CONFIG_DIR`: Directory for config files (default: /app/config)
- `ADMIN_PASSWORD`: Initial admin password (default: admin123)

## Common Patterns

### Adding New Protocol Support

1. Define config struct in `backend/models/proxy.go` (e.g., `TrojanConfig`)
2. Add parser in `backend/services/sharelink.go` (e.g., `parseTrojanLink`)
3. Add outbound generator in `backend/services/singbox.go` (e.g., `generateTrojanOutbound`)
4. Update `generateOutbound()` switch case
5. Update `ParseShareLink()` to recognize new protocol prefix

### Modifying Sing-box Config Structure

When sing-box config format changes:
1. Update structs in `backend/services/singbox.go` (e.g., `InboundConfig`, `OutboundConfig`)
2. Modify protocol-specific generators (e.g., `generateVLESSOutbound`)
3. Use `Extra` map for dynamic/optional fields
4. Update `marshalConfig()` to merge `Extra` fields correctly

### Adding API Endpoints

1. Add handler method to `Handler` in `backend/api/handlers.go`
2. Register route in `backend/main.go` (public or protected)
3. Update frontend API client in `frontend/src/utils/api.js`
4. Create/update React component to consume endpoint

## Network Architecture Notes

- **Host networking required**: Docker must use `network_mode: host` for multi-port listening
- **Port conflicts**: Each node's inbound port must be unique and available on host
- **IPv6 support**: Inbounds listen on `::` (dual-stack)
- **Routing**: Simple 1:1 mapping (one inbound → one outbound per node)
- **DNS**: Optional DNS configuration in sing-box config (currently not used)

## Security Considerations

- Admin password stored as bcrypt hash in database
- Proxy authentication (HTTP/SOCKS5) sent in plaintext to sing-box
- Session tokens stored in memory (lost on restart)
- No HTTPS on management interface by default (use reverse proxy for production)
- All proxy configs stored unencrypted in SQLite database

