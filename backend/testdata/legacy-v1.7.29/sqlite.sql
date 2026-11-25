CREATE TABLE proxy_nodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    remark TEXT DEFAULT '',
    type TEXT NOT NULL,
    config TEXT NOT NULL,
    inbound_port INTEGER NOT NULL,
    inbound_port_pinned INTEGER NOT NULL DEFAULT 0,
    username TEXT DEFAULT '',
    password TEXT DEFAULT '',
    tcp_reuse_enabled INTEGER NOT NULL DEFAULT 1,
    upstream_mode TEXT NOT NULL DEFAULT 'none',
    upstream_type TEXT NOT NULL DEFAULT '',
    upstream_config TEXT NOT NULL DEFAULT '',
    upstream_ip TEXT NOT NULL DEFAULT '',
    upstream_location TEXT NOT NULL DEFAULT '',
    upstream_country_code TEXT NOT NULL DEFAULT '',
    upstream_latency INTEGER NOT NULL DEFAULT 0,
    upstream_error TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL,
    node_ip TEXT DEFAULT '',
    location TEXT DEFAULT '',
    country_code TEXT DEFAULT '',
    latency INTEGER DEFAULT 0,
    enabled INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    singleton_key INTEGER NOT NULL DEFAULT 1,
    admin_password TEXT NOT NULL,
    admin_password_env_fingerprint TEXT NOT NULL DEFAULT '',
    auth_generation INTEGER NOT NULL DEFAULT 0,
    start_port INTEGER DEFAULT 10000,
    preserve_inbound_ports INTEGER NOT NULL DEFAULT 0,
    global_upstream_enabled INTEGER NOT NULL DEFAULT 0,
    global_upstream_type TEXT NOT NULL DEFAULT '',
    global_upstream_config TEXT NOT NULL DEFAULT '',
    global_upstream_ip TEXT NOT NULL DEFAULT '',
    global_upstream_location TEXT NOT NULL DEFAULT '',
    global_upstream_country_code TEXT NOT NULL DEFAULT '',
    global_upstream_latency INTEGER NOT NULL DEFAULT 0,
    global_upstream_error TEXT NOT NULL DEFAULT '',
    node_list_column_widths TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE admin_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL,
    auth_generation INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL,
    user_agent TEXT DEFAULT '',
    ip TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_settings_singleton_key ON settings(singleton_key);
CREATE UNIQUE INDEX idx_admin_sessions_token_hash ON admin_sessions(token_hash);

CREATE TABLE manager_instance_lock (
    singleton_key INTEGER PRIMARY KEY,
    owner_id TEXT NOT NULL,
    lease_expires_at BIGINT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
