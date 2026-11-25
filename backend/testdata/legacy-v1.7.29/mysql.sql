CREATE TABLE proxy_nodes (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    remark VARCHAR(1024) NOT NULL DEFAULT '',
    type VARCHAR(64) NOT NULL,
    config LONGTEXT NOT NULL,
    inbound_port INTEGER NOT NULL,
    inbound_port_pinned BOOLEAN NOT NULL DEFAULT FALSE,
    username VARCHAR(255) NOT NULL DEFAULT '',
    password VARCHAR(255) NOT NULL DEFAULT '',
    tcp_reuse_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    upstream_mode VARCHAR(16) NOT NULL DEFAULT 'none',
    upstream_type VARCHAR(64) NOT NULL DEFAULT '',
    upstream_config LONGTEXT NOT NULL,
    upstream_ip VARCHAR(255) NOT NULL DEFAULT '',
    upstream_location VARCHAR(255) NOT NULL DEFAULT '',
    upstream_country_code VARCHAR(32) NOT NULL DEFAULT '',
    upstream_latency INTEGER NOT NULL DEFAULT 0,
    upstream_error VARCHAR(2048) NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL,
    node_ip VARCHAR(255) NOT NULL DEFAULT '',
    location VARCHAR(255) NOT NULL DEFAULT '',
    country_code VARCHAR(32) NOT NULL DEFAULT '',
    latency INTEGER DEFAULT 0,
    enabled BOOLEAN DEFAULT TRUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE settings (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    singleton_key SMALLINT NOT NULL DEFAULT 1,
    admin_password TEXT NOT NULL,
    admin_password_env_fingerprint VARCHAR(128) NOT NULL DEFAULT '',
    auth_generation BIGINT NOT NULL DEFAULT 0,
    start_port INTEGER DEFAULT 10000,
    preserve_inbound_ports BOOLEAN NOT NULL DEFAULT FALSE,
    global_upstream_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    global_upstream_type VARCHAR(64) NOT NULL DEFAULT '',
    global_upstream_config LONGTEXT NOT NULL,
    global_upstream_ip VARCHAR(255) NOT NULL DEFAULT '',
    global_upstream_location VARCHAR(255) NOT NULL DEFAULT '',
    global_upstream_country_code VARCHAR(32) NOT NULL DEFAULT '',
    global_upstream_latency INTEGER NOT NULL DEFAULT 0,
    global_upstream_error VARCHAR(2048) NOT NULL DEFAULT '',
    node_list_column_widths TEXT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE admin_sessions (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    token_hash VARCHAR(128) NOT NULL,
    auth_generation BIGINT NOT NULL DEFAULT 0,
    expires_at BIGINT NOT NULL,
    user_agent VARCHAR(512) NOT NULL DEFAULT '',
    ip VARCHAR(128) NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_settings_singleton_key ON settings(singleton_key);
CREATE UNIQUE INDEX idx_admin_sessions_token_hash ON admin_sessions(token_hash);

CREATE TABLE manager_instance_lock (
    singleton_key SMALLINT PRIMARY KEY,
    owner_id VARCHAR(64) NOT NULL,
    lease_expires_at BIGINT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
