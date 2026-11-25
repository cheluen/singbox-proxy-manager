CREATE TABLE proxy_nodes (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    remark TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL,
    config TEXT NOT NULL,
    inbound_port INTEGER NOT NULL,
    inbound_port_pinned BOOLEAN NOT NULL DEFAULT FALSE,
    username TEXT NOT NULL DEFAULT '',
    password TEXT NOT NULL DEFAULT '',
    tcp_reuse_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    upstream_mode TEXT NOT NULL DEFAULT 'none',
    upstream_type TEXT NOT NULL DEFAULT '',
    upstream_config TEXT NOT NULL DEFAULT '',
    upstream_ip TEXT NOT NULL DEFAULT '',
    upstream_location TEXT NOT NULL DEFAULT '',
    upstream_country_code TEXT NOT NULL DEFAULT '',
    upstream_latency INTEGER NOT NULL DEFAULT 0,
    upstream_error TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL,
    node_ip TEXT NOT NULL DEFAULT '',
    location TEXT NOT NULL DEFAULT '',
    country_code TEXT NOT NULL DEFAULT '',
    latency INTEGER DEFAULT 0,
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE settings (
    id BIGSERIAL PRIMARY KEY,
    singleton_key SMALLINT NOT NULL DEFAULT 1,
    admin_password TEXT NOT NULL,
    admin_password_env_fingerprint TEXT NOT NULL DEFAULT '',
    auth_generation BIGINT NOT NULL DEFAULT 0,
    start_port INTEGER DEFAULT 10000,
    preserve_inbound_ports BOOLEAN NOT NULL DEFAULT FALSE,
    global_upstream_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    global_upstream_type TEXT NOT NULL DEFAULT '',
    global_upstream_config TEXT NOT NULL DEFAULT '',
    global_upstream_ip TEXT NOT NULL DEFAULT '',
    global_upstream_location TEXT NOT NULL DEFAULT '',
    global_upstream_country_code TEXT NOT NULL DEFAULT '',
    global_upstream_latency INTEGER NOT NULL DEFAULT 0,
    global_upstream_error TEXT NOT NULL DEFAULT '',
    node_list_column_widths TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE admin_sessions (
    id BIGSERIAL PRIMARY KEY,
    token_hash TEXT NOT NULL,
    auth_generation BIGINT NOT NULL DEFAULT 0,
    expires_at BIGINT NOT NULL,
    user_agent TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_settings_singleton_key ON settings(singleton_key);
CREATE UNIQUE INDEX idx_admin_sessions_token_hash ON admin_sessions(token_hash);

CREATE TABLE manager_instance_lock (
    singleton_key SMALLINT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    lease_expires_at BIGINT NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);
