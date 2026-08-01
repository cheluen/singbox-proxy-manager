package models

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	appdb "sb-proxy/backend/database"

	"golang.org/x/crypto/bcrypt"
)

// ProxyNode represents a proxy node configuration
type ProxyNode struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Remark      string `json:"remark"`
	Type        string `json:"type"`   // ss, vless, vmess, hy2, tuic, trojan, anytls, socks5, socks5h, http, wireguard, direct
	Config      string `json:"config"` // JSON string of protocol-specific config
	InboundPort int    `json:"inbound_port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	// TCPReuseEnabled controls whether username+route-number routing can target this node.
	TCPReuseEnabled bool      `json:"tcp_reuse_enabled"`
	SortOrder       int       `json:"sort_order"`
	NodeIP          string    `json:"node_ip"`
	Location        string    `json:"location"`
	CountryCode     string    `json:"country_code"`
	Latency         int       `json:"latency"` // in milliseconds
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// NativeOptions preserves a native sing-box options object without narrowing
// fields that are version-specific or accept multiple JSON representations.
// The generator deep-merges these objects with the legacy flattened fields.
type NativeOptions map[string]interface{}

// ListableString accepts either the sing-box shorthand string form or the full
// string-array form used by listable options such as network and server_ports.
type ListableString []string

func (l *ListableString) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			*l = nil
		} else {
			*l = ListableString{single}
		}
		return nil
	}

	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*l = ListableString(list)
	return nil
}

// ByteList keeps WireGuard reserved bytes in sing-box's documented numeric
// array form while remaining able to read historical base64 JSON produced by
// encoding/json for []byte/[]uint8 fields.
type ByteList []byte

func (b ByteList) MarshalJSON() ([]byte, error) {
	values := make([]int, len(b))
	for i, value := range b {
		values[i] = int(value)
	}
	return json.Marshal(values)
}

func (b *ByteList) UnmarshalJSON(data []byte) error {
	var values []uint8
	if err := json.Unmarshal(data, &values); err == nil {
		*b = ByteList(values)
		return nil
	}

	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return fmt.Errorf("wireguard reserved must be a byte array or base64 string: %w", err)
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, err := encoding.DecodeString(encoded)
		if err == nil {
			*b = ByteList(decoded)
			return nil
		}
	}
	return fmt.Errorf("wireguard reserved contains invalid base64 data")
}

// DialerOptions mirrors sing-box v1.12.12 option.DialerOptions. Union-shaped
// native values use interface{} so their number/string or string/object forms
// survive the manager's application-level JSON representation losslessly.
type DialerOptions struct {
	Detour              string         `json:"detour,omitempty"`
	BindInterface       string         `json:"bind_interface,omitempty"`
	Inet4BindAddress    string         `json:"inet4_bind_address,omitempty"`
	Inet6BindAddress    string         `json:"inet6_bind_address,omitempty"`
	ProtectPath         string         `json:"protect_path,omitempty"`
	RoutingMark         interface{}    `json:"routing_mark,omitempty"`
	ReuseAddr           bool           `json:"reuse_addr,omitempty"`
	NetNS               string         `json:"netns,omitempty"`
	ConnectTimeout      string         `json:"connect_timeout,omitempty"`
	TCPFastOpen         bool           `json:"tcp_fast_open,omitempty"`
	TCPMultiPath        bool           `json:"tcp_multi_path,omitempty"`
	UDPFragment         *bool          `json:"udp_fragment,omitempty"`
	DomainResolver      interface{}    `json:"domain_resolver,omitempty"`
	NetworkStrategy     string         `json:"network_strategy,omitempty"`
	NetworkType         ListableString `json:"network_type,omitempty"`
	FallbackNetworkType ListableString `json:"fallback_network_type,omitempty"`
	FallbackDelay       string         `json:"fallback_delay,omitempty"`
	DomainStrategy      string         `json:"domain_strategy,omitempty"`
}

// DirectConfig represents sing-box direct outbound configuration.
// It supports optional destination overrides (deprecated in sing-box v1.12.x but still accepted).
type DirectConfig struct {
	DialerOptions
	OverrideAddress string `json:"override_address,omitempty"`
	OverridePort    int    `json:"override_port,omitempty"`
	ProxyProtocol   int    `json:"proxy_protocol,omitempty"`
}

// SSConfig represents Shadowsocks configuration
type SSConfig struct {
	DialerOptions
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Method     string `json:"method"`
	Password   string `json:"password"`
	Plugin     string `json:"plugin,omitempty"`
	PluginOpts string `json:"plugin_opts,omitempty"`
	// Additional parameters
	Network           ListableString         `json:"network,omitempty"`
	UDPOverTCP        interface{}            `json:"udp_over_tcp,omitempty"`
	UDPOverTCPOptions NativeOptions          `json:"udp_over_tcp_options,omitempty"`
	MultiplexConfig   map[string]interface{} `json:"multiplex,omitempty"`
}

// VLESSConfig represents VLESS configuration
type VLESSConfig struct {
	DialerOptions
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	UUID       string `json:"uuid"`
	Flow       string `json:"flow,omitempty"`
	Encryption string `json:"encryption,omitempty"`
	Network    string `json:"network,omitempty"` // tcp, kcp, ws, http, quic, grpc, httpupgrade
	// OutboundNetwork is the native sing-box tcp/udp limiter. Network remains
	// the legacy transport-type field for backward compatibility.
	OutboundNetwork ListableString `json:"outbound_network,omitempty"`
	Security        string         `json:"security,omitempty"` // none, tls, reality
	SNI             string         `json:"sni,omitempty"`
	ALPN            string         `json:"alpn,omitempty"`
	Fingerprint     string         `json:"fingerprint,omitempty"`
	PublicKey       string         `json:"public_key,omitempty"`
	ShortID         string         `json:"short_id,omitempty"`
	SpiderX         string         `json:"spider_x,omitempty"`
	Insecure        bool           `json:"insecure,omitempty"`
	// WebSocket options
	Path            string            `json:"path,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Host            string            `json:"host,omitempty"`
	MaxEarlyData    int               `json:"max_early_data,omitempty"`
	EarlyDataHeader string            `json:"early_data_header,omitempty"`
	// gRPC options
	ServiceName string `json:"service_name,omitempty"`
	// TCP/KCP/QUIC options
	HeaderType string `json:"header_type,omitempty"`
	Seed       string `json:"seed,omitempty"`
	// HTTPUpgrade options
	HTTPUpgradePath string `json:"http_upgrade_path,omitempty"`
	HTTPUpgradeHost string `json:"http_upgrade_host,omitempty"`
	// Packet encoding
	PacketEncoding string `json:"packet_encoding,omitempty"`
	// Multiplex
	MultiplexConfig  map[string]interface{} `json:"multiplex,omitempty"`
	TLSOptions       NativeOptions          `json:"tls_options,omitempty"`
	TransportOptions NativeOptions          `json:"transport_options,omitempty"`
}

// VMESSConfig represents VMess configuration
type VMESSConfig struct {
	DialerOptions
	Server          string         `json:"server"`
	ServerPort      int            `json:"server_port"`
	UUID            string         `json:"uuid"`
	AlterID         int            `json:"alter_id"`
	Security        string         `json:"security,omitempty"` // auto, aes-128-gcm, chacha20-poly1305, none, zero
	Network         string         `json:"network,omitempty"`  // tcp, kcp, ws, http, quic, grpc, httpupgrade
	OutboundNetwork ListableString `json:"outbound_network,omitempty"`
	TLS             string         `json:"tls,omitempty"` // none, tls
	SNI             string         `json:"sni,omitempty"`
	ALPN            string         `json:"alpn,omitempty"`
	Fingerprint     string         `json:"fingerprint,omitempty"`
	Insecure        bool           `json:"insecure,omitempty"`
	// WebSocket options
	Path            string            `json:"path,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Host            string            `json:"host,omitempty"`
	MaxEarlyData    int               `json:"max_early_data,omitempty"`
	EarlyDataHeader string            `json:"early_data_header,omitempty"`
	// gRPC options
	ServiceName string `json:"service_name,omitempty"`
	// HTTP options
	Method   string   `json:"method,omitempty"`
	HTTPPath []string `json:"http_path,omitempty"`
	// TCP/KCP/QUIC options
	HeaderType string `json:"header_type,omitempty"`
	Seed       string `json:"seed,omitempty"`
	// HTTPUpgrade options
	HTTPUpgradePath string `json:"http_upgrade_path,omitempty"`
	HTTPUpgradeHost string `json:"http_upgrade_host,omitempty"`
	// Packet encoding
	PacketEncoding string `json:"packet_encoding,omitempty"`
	// Global padding
	GlobalPadding       bool `json:"global_padding,omitempty"`
	AuthenticatedLength bool `json:"authenticated_length,omitempty"`
	// Multiplex
	MultiplexConfig  map[string]interface{} `json:"multiplex,omitempty"`
	TLSOptions       NativeOptions          `json:"tls_options,omitempty"`
	TransportOptions NativeOptions          `json:"transport_options,omitempty"`
}

// Hysteria2Config represents Hysteria2 configuration
type Hysteria2Config struct {
	DialerOptions
	Server             string         `json:"server"`
	ServerPort         int            `json:"server_port"`
	ServerPorts        ListableString `json:"server_ports,omitempty"`
	Password           string         `json:"password"`
	UpMbps             int            `json:"up_mbps,omitempty"`
	DownMbps           int            `json:"down_mbps,omitempty"`
	Obfs               interface{}    `json:"obfs,omitempty"`
	ObfsPassword       string         `json:"obfs_password,omitempty"`
	SNI                string         `json:"sni,omitempty"`
	ALPN               []string       `json:"alpn,omitempty"`
	Fingerprint        string         `json:"fingerprint,omitempty"`
	InsecureSkipVerify bool           `json:"insecure_skip_verify,omitempty"`
	// Additional Hysteria2 parameters
	SalamanderPassword string         `json:"salamander_password,omitempty"`
	BrutalDownMbps     int            `json:"brutal_down_mbps,omitempty"`
	BrutalUpMbps       int            `json:"brutal_up_mbps,omitempty"`
	Network            ListableString `json:"network,omitempty"` // tcp or udp
	HopInterval        string         `json:"hop_interval,omitempty"`
	BrutalDebug        bool           `json:"brutal_debug,omitempty"`
	TLSOptions         NativeOptions  `json:"tls_options,omitempty"`
}

// TrojanConfig represents Trojan configuration
type TrojanConfig struct {
	DialerOptions
	Server           string                 `json:"server"`
	ServerPort       int                    `json:"server_port"`
	Password         string                 `json:"password"`
	Security         string                 `json:"security,omitempty"` // none, tls, reality; empty keeps legacy TLS default
	Network          string                 `json:"network,omitempty"`  // tcp, ws, grpc, http, httpupgrade
	OutboundNetwork  ListableString         `json:"outbound_network,omitempty"`
	SNI              string                 `json:"sni,omitempty"`
	ALPN             []string               `json:"alpn,omitempty"`
	Fingerprint      string                 `json:"fingerprint,omitempty"`
	Insecure         bool                   `json:"insecure,omitempty"`
	Host             string                 `json:"host,omitempty"`         // ws/http Host header
	Path             string                 `json:"path,omitempty"`         // ws/http path
	ServiceName      string                 `json:"service_name,omitempty"` // grpc service name
	HTTPMethod       string                 `json:"method,omitempty"`       // http/h2 method
	Headers          map[string]string      `json:"headers,omitempty"`      // transport headers
	MultiplexConfig  map[string]interface{} `json:"multiplex,omitempty"`
	TLSOptions       NativeOptions          `json:"tls_options,omitempty"`
	TransportOptions NativeOptions          `json:"transport_options,omitempty"`
}

// TUICConfig represents TUIC configuration
type TUICConfig struct {
	DialerOptions
	Server             string   `json:"server"`
	ServerPort         int      `json:"server_port"`
	UUID               string   `json:"uuid"`
	Password           string   `json:"password"`
	CongestionControl  string   `json:"congestion_control,omitempty"` // cubic, new_reno, bbr
	UDPRelayMode       string   `json:"udp_relay_mode,omitempty"`     // native, quic
	SNI                string   `json:"sni,omitempty"`
	ALPN               []string `json:"alpn,omitempty"`
	Fingerprint        string   `json:"fingerprint,omitempty"`
	InsecureSkipVerify bool     `json:"insecure_skip_verify,omitempty"`
	ZeroRTTHandshake   bool     `json:"zero_rtt_handshake,omitempty"`
	UDPOverStream      bool     `json:"udp_over_stream,omitempty"`
	Heartbeat          string   `json:"heartbeat,omitempty"`
	// Additional TUIC parameters
	Network    ListableString `json:"network,omitempty"` // tcp or udp
	DisableSNI bool           `json:"disable_sni,omitempty"`
	ReduceRTT  bool           `json:"reduce_rtt,omitempty"`
	TLSOptions NativeOptions  `json:"tls_options,omitempty"`
}

// AnyTLSConfig represents AnyTLS configuration
type AnyTLSConfig struct {
	DialerOptions
	Server                   string        `json:"server"`
	ServerPort               int           `json:"server_port"`
	Password                 string        `json:"password"`
	SNI                      string        `json:"sni,omitempty"`
	ALPN                     []string      `json:"alpn,omitempty"`
	Fingerprint              string        `json:"fingerprint,omitempty"`
	Insecure                 bool          `json:"insecure,omitempty"`
	IdleSessionCheckInterval string        `json:"idle_session_check_interval,omitempty"`
	IdleSessionTimeout       string        `json:"idle_session_timeout,omitempty"`
	MinIdleSession           int           `json:"min_idle_session,omitempty"`
	TLSOptions               NativeOptions `json:"tls_options,omitempty"`
}

// SOCKS5Config represents SOCKS5 proxy configuration
type SOCKS5Config struct {
	DialerOptions
	Server            string         `json:"server"`
	ServerPort        int            `json:"server_port"`
	Username          string         `json:"username,omitempty"`
	Password          string         `json:"password,omitempty"`
	Network           ListableString `json:"network,omitempty"`
	UDPOverTCP        interface{}    `json:"udp_over_tcp,omitempty"`
	UDPOverTCPOptions NativeOptions  `json:"udp_over_tcp_options,omitempty"`
}

// HTTPProxyConfig represents HTTP/HTTPS proxy configuration
type HTTPProxyConfig struct {
	DialerOptions
	Server     string        `json:"server"`
	ServerPort int           `json:"server_port"`
	Username   string        `json:"username,omitempty"`
	Password   string        `json:"password,omitempty"`
	TLS        bool          `json:"tls,omitempty"`
	Insecure   bool          `json:"insecure,omitempty"`
	SNI        string        `json:"sni,omitempty"`
	Path       string        `json:"path,omitempty"`
	Headers    NativeOptions `json:"headers,omitempty"`
	TLSOptions NativeOptions `json:"tls_options,omitempty"`
}

// WireGuardPeerConfig represents a single peer configuration for WireGuard.
type WireGuardPeerConfig struct {
	Server                      string   `json:"server,omitempty"`
	ServerPort                  int      `json:"server_port,omitempty"`
	PublicKey                   string   `json:"public_key"`
	PreSharedKey                string   `json:"pre_shared_key,omitempty"`
	AllowedIPs                  []string `json:"allowed_ips,omitempty"`
	Reserved                    ByteList `json:"reserved,omitempty"`
	PersistentKeepaliveInterval int      `json:"persistent_keepalive_interval,omitempty"`
}

// WireGuardConfig represents sing-box wireguard outbound configuration.
// The structure keeps a small set of app-level compatibility fields
// (allowed_ips, domain_resolver_strategy) that are converted when generating
// the final sing-box config.
type WireGuardConfig struct {
	Server                 string                `json:"server,omitempty"`
	ServerPort             int                   `json:"server_port,omitempty"`
	SystemInterface        bool                  `json:"system_interface,omitempty"`
	InterfaceName          string                `json:"interface_name,omitempty"`
	LocalAddress           []string              `json:"local_address"`
	PrivateKey             string                `json:"private_key"`
	PeerPublicKey          string                `json:"peer_public_key,omitempty"`
	PreSharedKey           string                `json:"pre_shared_key,omitempty"`
	AllowedIPs             []string              `json:"allowed_ips,omitempty"`
	Reserved               ByteList              `json:"reserved,omitempty"`
	Workers                int                   `json:"workers,omitempty"`
	MTU                    int                   `json:"mtu,omitempty"`
	ListenPort             int                   `json:"listen_port,omitempty"`
	UDPTimeout             string                `json:"udp_timeout,omitempty"`
	Network                string                `json:"network,omitempty"`
	Detour                 string                `json:"detour,omitempty"`
	DomainResolver         interface{}           `json:"domain_resolver,omitempty"`
	DomainResolverStrategy string                `json:"domain_resolver_strategy,omitempty"`
	RoutingMark            interface{}           `json:"routing_mark,omitempty"`
	UDPFragment            *bool                 `json:"udp_fragment,omitempty"`
	ConnectTimeout         string                `json:"connect_timeout,omitempty"`
	BindInterface          string                `json:"bind_interface,omitempty"`
	Inet4BindAddress       string                `json:"inet4_bind_address,omitempty"`
	Inet6BindAddress       string                `json:"inet6_bind_address,omitempty"`
	ProtectPath            string                `json:"protect_path,omitempty"`
	ReuseAddr              bool                  `json:"reuse_addr,omitempty"`
	NetNS                  string                `json:"netns,omitempty"`
	TCPFastOpen            bool                  `json:"tcp_fast_open,omitempty"`
	TCPMultiPath           bool                  `json:"tcp_multi_path,omitempty"`
	DomainResolverOptions  NativeOptions         `json:"domain_resolver_options,omitempty"`
	NetworkStrategy        string                `json:"network_strategy,omitempty"`
	NetworkType            ListableString        `json:"network_type,omitempty"`
	FallbackNetworkType    ListableString        `json:"fallback_network_type,omitempty"`
	FallbackDelay          string                `json:"fallback_delay,omitempty"`
	DomainStrategy         string                `json:"domain_strategy,omitempty"`
	Peers                  []WireGuardPeerConfig `json:"peers,omitempty"`
}

// Settings represents global settings
type Settings struct {
	ID                   int       `json:"id"`
	AdminPassword        string    `json:"admin_password"`
	AdminPasswordSet     int       `json:"admin_password_set"`
	StartPort            int       `json:"start_port"`
	PreserveInboundPorts bool      `json:"preserve_inbound_ports"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// InitDB initializes the database
func InitDB(db *sql.DB) error {
	dialect := appdb.DialectFor(db)
	if err := createSchema(db, dialect); err != nil {
		return err
	}

	// Insert default settings if not exists
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM settings").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		envPassword := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD"))
		if envPassword != "" {
			hashedPassword, err := bcrypt.GenerateFromPassword([]byte(envPassword), bcrypt.DefaultCost)
			if err != nil {
				log.Printf("Failed to hash initial admin password: %v", err)
				return err
			}
			_, err = db.Exec("INSERT INTO settings (admin_password, admin_password_set, start_port, preserve_inbound_ports) VALUES (?, ?, ?, ?)", string(hashedPassword), 1, 30001, false)
			if err != nil {
				return err
			}
			log.Println("Admin password is managed by ADMIN_PASSWORD (env) and has been hashed")
		} else {
			_, err = db.Exec("INSERT INTO settings (admin_password, admin_password_set, start_port, preserve_inbound_ports) VALUES (?, ?, ?, ?)", "", 0, 30001, false)
			if err != nil {
				return err
			}
			log.Println("No admin password configured; setup is required before first login")
		}
	} else {
		// Self-heal legacy default password and migration state.
		var settingsID int
		var adminPassword string
		var adminPasswordSet int
		if err := db.QueryRow("SELECT id, admin_password, admin_password_set FROM settings LIMIT 1").Scan(&settingsID, &adminPassword, &adminPasswordSet); err != nil {
			return err
		}

		// If stored admin_password is not a bcrypt hash, force reset.
		if adminPassword != "" && !strings.HasPrefix(adminPassword, "$2") {
			if _, err := db.Exec("UPDATE settings SET admin_password = '', admin_password_set = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?", settingsID); err != nil {
				return err
			}
			log.Println("Admin password storage format is invalid; setup is required")
		} else if adminPassword != "" {
			// If the stored hash matches the insecure legacy default, force reset.
			if err := bcrypt.CompareHashAndPassword([]byte(adminPassword), []byte("admin123")); err == nil {
				if _, err := db.Exec("UPDATE settings SET admin_password = '', admin_password_set = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?", settingsID); err != nil {
					return err
				}
				log.Println("Legacy default admin password detected; setup is required")
			} else if adminPasswordSet == 0 {
				// Mark as set for existing non-default password.
				_, _ = db.Exec("UPDATE settings SET admin_password_set = 1 WHERE id = ?", settingsID)
			}
		} else if adminPasswordSet != 0 {
			_, _ = db.Exec("UPDATE settings SET admin_password_set = 0 WHERE id = ?", settingsID)
		}

		// If ADMIN_PASSWORD is provided, it becomes the source of truth and will forcibly
		// overwrite the stored password on every start/restart. This guarantees recovery
		// access even when the admin password is forgotten.
		envPassword := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD"))
		if envPassword != "" {
			var currentHash string
			var currentSet int
			if err := db.QueryRow("SELECT admin_password, admin_password_set FROM settings LIMIT 1").Scan(&currentHash, &currentSet); err != nil {
				return err
			}

			needReset := strings.TrimSpace(currentHash) == ""
			if !needReset {
				if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(envPassword)); err != nil {
					needReset = true
				}
			}

			if needReset {
				hashedPassword, err := bcrypt.GenerateFromPassword([]byte(envPassword), bcrypt.DefaultCost)
				if err != nil {
					log.Printf("Failed to hash admin password from ADMIN_PASSWORD: %v", err)
					return err
				}
				if _, err := db.Exec(
					"UPDATE settings SET admin_password = ?, admin_password_set = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
					string(hashedPassword),
					settingsID,
				); err != nil {
					return err
				}
				// Revoke all sessions after password reset to force re-login.
				_, _ = db.Exec("DELETE FROM admin_sessions")
				log.Println("Admin password has been reset from ADMIN_PASSWORD (env); all sessions revoked")
			} else if currentSet == 0 {
				_, _ = db.Exec("UPDATE settings SET admin_password_set = 1 WHERE id = ?", settingsID)
			}
		}
	}

	// Clean up expired sessions opportunistically.
	now := time.Now().Unix()
	_, _ = db.Exec("DELETE FROM admin_sessions WHERE expires_at <= ?", now)

	return nil
}

func createSchema(db *sql.DB, dialect appdb.Dialect) error {
	for _, statement := range schemaStatements(dialect) {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}

	for _, column := range migrationColumns(dialect) {
		if err := ensureColumn(db, dialect, column.Table, column.Name, column.AlterSQL); err != nil {
			return err
		}
	}
	if dialect == appdb.DialectMySQL {
		if err := ensureMySQLConfigLongText(db); err != nil {
			return err
		}
	}

	return ensureUniqueIndex(db, dialect, "idx_admin_sessions_token_hash", "admin_sessions", "token_hash")
}

func ensureMySQLConfigLongText(db *sql.DB) error {
	var dataType string
	err := db.QueryRow(`
		SELECT data_type
		FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND table_name = 'proxy_nodes'
		  AND column_name = 'config'
	`).Scan(&dataType)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(dataType), "longtext") {
		return nil
	}
	_, err = db.Exec("ALTER TABLE proxy_nodes MODIFY COLUMN config LONGTEXT NOT NULL")
	return err
}

type migrationColumn struct {
	Table    string
	Name     string
	AlterSQL string
}

func schemaStatements(dialect appdb.Dialect) []string {
	switch dialect {
	case appdb.DialectPostgres:
		return []string{
			`CREATE TABLE IF NOT EXISTS proxy_nodes (
				id BIGSERIAL PRIMARY KEY,
				name TEXT NOT NULL,
				remark TEXT NOT NULL DEFAULT '',
				type TEXT NOT NULL,
				config TEXT NOT NULL,
				inbound_port INTEGER NOT NULL,
				username TEXT NOT NULL DEFAULT '',
				password TEXT NOT NULL DEFAULT '',
				tcp_reuse_enabled BOOLEAN NOT NULL DEFAULT TRUE,
				sort_order INTEGER NOT NULL,
				node_ip TEXT NOT NULL DEFAULT '',
				location TEXT NOT NULL DEFAULT '',
				country_code TEXT NOT NULL DEFAULT '',
				latency INTEGER DEFAULT 0,
				enabled BOOLEAN DEFAULT TRUE,
				created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE TABLE IF NOT EXISTS settings (
				id BIGSERIAL PRIMARY KEY,
				admin_password TEXT NOT NULL,
				admin_password_set INTEGER DEFAULT 0,
				start_port INTEGER DEFAULT 10000,
				preserve_inbound_ports BOOLEAN NOT NULL DEFAULT FALSE,
				created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE TABLE IF NOT EXISTS admin_sessions (
				id BIGSERIAL PRIMARY KEY,
				token_hash TEXT NOT NULL,
				expires_at BIGINT NOT NULL,
				user_agent TEXT NOT NULL DEFAULT '',
				ip TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
			)`,
		}
	case appdb.DialectMySQL:
		return []string{
			`CREATE TABLE IF NOT EXISTS proxy_nodes (
				id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
				name VARCHAR(255) NOT NULL,
				remark VARCHAR(1024) NOT NULL DEFAULT '',
				type VARCHAR(64) NOT NULL,
				config LONGTEXT NOT NULL,
				inbound_port INTEGER NOT NULL,
				username VARCHAR(255) NOT NULL DEFAULT '',
				password VARCHAR(255) NOT NULL DEFAULT '',
				tcp_reuse_enabled BOOLEAN NOT NULL DEFAULT TRUE,
				sort_order INTEGER NOT NULL,
				node_ip VARCHAR(255) NOT NULL DEFAULT '',
				location VARCHAR(255) NOT NULL DEFAULT '',
				country_code VARCHAR(32) NOT NULL DEFAULT '',
				latency INTEGER DEFAULT 0,
				enabled BOOLEAN DEFAULT TRUE,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE TABLE IF NOT EXISTS settings (
				id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
				admin_password TEXT NOT NULL,
				admin_password_set INTEGER DEFAULT 0,
				start_port INTEGER DEFAULT 10000,
				preserve_inbound_ports BOOLEAN NOT NULL DEFAULT FALSE,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE TABLE IF NOT EXISTS admin_sessions (
				id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
				token_hash VARCHAR(128) NOT NULL,
				expires_at BIGINT NOT NULL,
				user_agent VARCHAR(512) NOT NULL DEFAULT '',
				ip VARCHAR(128) NOT NULL DEFAULT '',
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
		}
	default:
		return []string{
			`CREATE TABLE IF NOT EXISTS proxy_nodes (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL,
				remark TEXT DEFAULT '',
				type TEXT NOT NULL,
				config TEXT NOT NULL,
				inbound_port INTEGER NOT NULL,
				username TEXT DEFAULT '',
				password TEXT DEFAULT '',
				tcp_reuse_enabled INTEGER NOT NULL DEFAULT 1,
				sort_order INTEGER NOT NULL,
				node_ip TEXT DEFAULT '',
				location TEXT DEFAULT '',
				country_code TEXT DEFAULT '',
				latency INTEGER DEFAULT 0,
				enabled INTEGER DEFAULT 1,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE TABLE IF NOT EXISTS settings (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				admin_password TEXT NOT NULL,
				admin_password_set INTEGER DEFAULT 0,
				start_port INTEGER DEFAULT 10000,
				preserve_inbound_ports INTEGER NOT NULL DEFAULT 0,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE TABLE IF NOT EXISTS admin_sessions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				token_hash TEXT NOT NULL,
				expires_at INTEGER NOT NULL,
				user_agent TEXT DEFAULT '',
				ip TEXT DEFAULT '',
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
		}
	}
}

func migrationColumns(dialect appdb.Dialect) []migrationColumn {
	switch dialect {
	case appdb.DialectPostgres:
		return []migrationColumn{
			{Table: "proxy_nodes", Name: "remark", AlterSQL: "ALTER TABLE proxy_nodes ADD COLUMN remark TEXT NOT NULL DEFAULT ''"},
			{Table: "proxy_nodes", Name: "tcp_reuse_enabled", AlterSQL: "ALTER TABLE proxy_nodes ADD COLUMN tcp_reuse_enabled BOOLEAN NOT NULL DEFAULT TRUE"},
			{Table: "settings", Name: "admin_password_set", AlterSQL: "ALTER TABLE settings ADD COLUMN admin_password_set INTEGER DEFAULT 0"},
			{Table: "settings", Name: "preserve_inbound_ports", AlterSQL: "ALTER TABLE settings ADD COLUMN preserve_inbound_ports BOOLEAN NOT NULL DEFAULT FALSE"},
		}
	case appdb.DialectMySQL:
		return []migrationColumn{
			{Table: "proxy_nodes", Name: "remark", AlterSQL: "ALTER TABLE proxy_nodes ADD COLUMN remark VARCHAR(1024) NOT NULL DEFAULT ''"},
			{Table: "proxy_nodes", Name: "tcp_reuse_enabled", AlterSQL: "ALTER TABLE proxy_nodes ADD COLUMN tcp_reuse_enabled BOOLEAN NOT NULL DEFAULT TRUE"},
			{Table: "settings", Name: "admin_password_set", AlterSQL: "ALTER TABLE settings ADD COLUMN admin_password_set INTEGER DEFAULT 0"},
			{Table: "settings", Name: "preserve_inbound_ports", AlterSQL: "ALTER TABLE settings ADD COLUMN preserve_inbound_ports BOOLEAN NOT NULL DEFAULT FALSE"},
		}
	default:
		return []migrationColumn{
			{Table: "proxy_nodes", Name: "remark", AlterSQL: "ALTER TABLE proxy_nodes ADD COLUMN remark TEXT DEFAULT ''"},
			{Table: "proxy_nodes", Name: "tcp_reuse_enabled", AlterSQL: "ALTER TABLE proxy_nodes ADD COLUMN tcp_reuse_enabled INTEGER NOT NULL DEFAULT 1"},
			{Table: "settings", Name: "admin_password_set", AlterSQL: "ALTER TABLE settings ADD COLUMN admin_password_set INTEGER DEFAULT 0"},
			{Table: "settings", Name: "preserve_inbound_ports", AlterSQL: "ALTER TABLE settings ADD COLUMN preserve_inbound_ports INTEGER NOT NULL DEFAULT 0"},
		}
	}
}

func ensureColumn(db *sql.DB, dialect appdb.Dialect, table string, column string, alterSQL string) error {
	exists, err := columnExists(db, dialect, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec(alterSQL)
	return err
}

func columnExists(db *sql.DB, dialect appdb.Dialect, table string, column string) (bool, error) {
	switch dialect {
	case appdb.DialectPostgres:
		var count int
		err := db.QueryRow(`
			SELECT COUNT(*)
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = ?
			  AND column_name = ?
		`, table, column).Scan(&count)
		return count > 0, err
	case appdb.DialectMySQL:
		var count int
		err := db.QueryRow(`
			SELECT COUNT(*)
			FROM information_schema.columns
			WHERE table_schema = DATABASE()
			  AND table_name = ?
			  AND column_name = ?
		`, table, column).Scan(&count)
		return count > 0, err
	default:
		rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			return false, err
		}
		defer rows.Close()

		for rows.Next() {
			var cid int
			var name string
			var colType string
			var notNull int
			var dflt sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
				return false, err
			}
			if name == column {
				return true, nil
			}
		}
		return false, rows.Err()
	}
}

func ensureUniqueIndex(db *sql.DB, dialect appdb.Dialect, indexName string, table string, column string) error {
	if dialect == appdb.DialectMySQL {
		var count int
		if err := db.QueryRow(`
			SELECT COUNT(*)
			FROM information_schema.statistics
			WHERE table_schema = DATABASE()
			  AND table_name = ?
			  AND index_name = ?
		`, table, indexName).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		_, err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s(%s)", indexName, table, column))
		return err
	}
	_, err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s(%s)", indexName, table, column))
	return err
}

// ParseConfig parses the config string based on proxy type
func (p *ProxyNode) ParseConfig() (interface{}, error) {
	var config interface{}
	switch p.Type {
	case "direct":
		config = &DirectConfig{}
	case "ss":
		config = &SSConfig{}
	case "vless":
		config = &VLESSConfig{}
	case "vmess":
		config = &VMESSConfig{}
	case "hy2":
		config = &Hysteria2Config{}
	case "tuic":
		config = &TUICConfig{}
	case "trojan":
		config = &TrojanConfig{}
	case "anytls":
		config = &AnyTLSConfig{}
	case "socks5", "socks5h":
		config = &SOCKS5Config{}
	case "http":
		config = &HTTPProxyConfig{}
	case "wireguard":
		config = &WireGuardConfig{}
	default:
		return nil, fmt.Errorf("unsupported proxy type: %s", p.Type)
	}

	decoder := json.NewDecoder(strings.NewReader(p.Config))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(config); err != nil {
		return nil, fmt.Errorf("invalid %s config: %w", p.Type, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("invalid %s config: trailing JSON value", p.Type)
		}
		return nil, fmt.Errorf("invalid %s config: %w", p.Type, err)
	}

	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(p.Config), &rawFields); err != nil {
		return nil, fmt.Errorf("invalid %s config: %w", p.Type, err)
	}
	reconcileNativeCompatibilityFields(config, rawFields)
	return config, nil
}

type nativeTLSCompatibilityFields struct {
	options     *NativeOptions
	mode        *string
	modeKey     string
	serverName  *string
	insecure    *bool
	alpnString  *string
	alpnList    *[]string
	fingerprint *string
	disableSNI  *bool
	publicKey   *string
	shortID     *string
}

type nativeTransportCompatibilityFields struct {
	options       *NativeOptions
	network       *string
	path          *string
	headers       *map[string]string
	host          *string
	maxEarlyData  *int
	earlyDataName *string
	serviceName   *string
	method        *string
	httpPaths     *[]string
	upgradePath   *string
	upgradeHost   *string
}

func reconcileNativeCompatibilityFields(config interface{}, rawFields map[string]json.RawMessage) {
	switch typed := config.(type) {
	case *VLESSConfig:
		reconcileNativeTLSCompatibility(rawFields, nativeTLSCompatibilityFields{
			options: &typed.TLSOptions, mode: &typed.Security, modeKey: "security",
			serverName: &typed.SNI, insecure: &typed.Insecure, alpnString: &typed.ALPN,
			fingerprint: &typed.Fingerprint, publicKey: &typed.PublicKey, shortID: &typed.ShortID,
		})
		reconcileNativeTransportCompatibility(rawFields, nativeTransportCompatibilityFields{
			options: &typed.TransportOptions, network: &typed.Network, path: &typed.Path,
			headers: &typed.Headers, host: &typed.Host, maxEarlyData: &typed.MaxEarlyData,
			earlyDataName: &typed.EarlyDataHeader, serviceName: &typed.ServiceName,
			upgradePath: &typed.HTTPUpgradePath, upgradeHost: &typed.HTTPUpgradeHost,
		})
	case *VMESSConfig:
		reconcileNativeTLSCompatibility(rawFields, nativeTLSCompatibilityFields{
			options: &typed.TLSOptions, mode: &typed.TLS, modeKey: "tls",
			serverName: &typed.SNI, insecure: &typed.Insecure, alpnString: &typed.ALPN,
			fingerprint: &typed.Fingerprint,
		})
		reconcileNativeTransportCompatibility(rawFields, nativeTransportCompatibilityFields{
			options: &typed.TransportOptions, network: &typed.Network, path: &typed.Path,
			headers: &typed.Headers, host: &typed.Host, maxEarlyData: &typed.MaxEarlyData,
			earlyDataName: &typed.EarlyDataHeader, serviceName: &typed.ServiceName,
			method: &typed.Method, httpPaths: &typed.HTTPPath,
			upgradePath: &typed.HTTPUpgradePath, upgradeHost: &typed.HTTPUpgradeHost,
		})
	case *TrojanConfig:
		reconcileNativeTLSCompatibility(rawFields, nativeTLSCompatibilityFields{
			options: &typed.TLSOptions, mode: &typed.Security, modeKey: "security",
			serverName: &typed.SNI, insecure: &typed.Insecure, alpnList: &typed.ALPN,
			fingerprint: &typed.Fingerprint,
		})
		reconcileNativeTransportCompatibility(rawFields, nativeTransportCompatibilityFields{
			options: &typed.TransportOptions, network: &typed.Network, path: &typed.Path,
			headers: &typed.Headers, host: &typed.Host, serviceName: &typed.ServiceName,
			method: &typed.HTTPMethod,
		})
	case *Hysteria2Config:
		reconcileNativeTLSCompatibility(rawFields, nativeTLSCompatibilityFields{
			options: &typed.TLSOptions, serverName: &typed.SNI,
			insecure: &typed.InsecureSkipVerify, alpnList: &typed.ALPN,
			fingerprint: &typed.Fingerprint,
		})
	case *TUICConfig:
		reconcileNativeTLSCompatibility(rawFields, nativeTLSCompatibilityFields{
			options: &typed.TLSOptions, serverName: &typed.SNI,
			insecure: &typed.InsecureSkipVerify, alpnList: &typed.ALPN,
			fingerprint: &typed.Fingerprint, disableSNI: &typed.DisableSNI,
		})
	case *AnyTLSConfig:
		reconcileNativeTLSCompatibility(rawFields, nativeTLSCompatibilityFields{
			options: &typed.TLSOptions, serverName: &typed.SNI,
			insecure: &typed.Insecure, alpnList: &typed.ALPN,
			fingerprint: &typed.Fingerprint,
		})
	case *HTTPProxyConfig:
		if rawConfigHasField(rawFields, "tls") && len(typed.TLSOptions) > 0 {
			typed.TLSOptions["enabled"] = typed.TLS
			if !typed.TLS {
				delete(typed.TLSOptions, "reality")
			}
		}
		reconcileNativeTLSCompatibility(rawFields, nativeTLSCompatibilityFields{
			options: &typed.TLSOptions, serverName: &typed.SNI, insecure: &typed.Insecure,
		})
	}
}

func reconcileNativeTLSCompatibility(rawFields map[string]json.RawMessage, fields nativeTLSCompatibilityFields) {
	if fields.options == nil {
		return
	}
	if len(*fields.options) == 0 {
		if fields.mode != nil && fields.modeKey != "" && rawConfigHasField(rawFields, fields.modeKey) {
			mode := strings.ToLower(strings.TrimSpace(*fields.mode))
			if mode != "reality" {
				if fields.publicKey != nil {
					*fields.publicKey = ""
				}
				if fields.shortID != nil {
					*fields.shortID = ""
				}
			}
		}
		return
	}
	options := *fields.options
	realityCompatible := true

	if fields.mode != nil && fields.modeKey != "" && rawConfigHasField(rawFields, fields.modeKey) {
		mode := strings.ToLower(strings.TrimSpace(*fields.mode))
		switch mode {
		case "", "none", "false":
			options["enabled"] = false
			delete(options, "reality")
			realityCompatible = false
		case "tls", "true", "xtls":
			options["enabled"] = true
			delete(options, "reality")
			realityCompatible = false
		case "reality":
			options["enabled"] = true
		}
	}
	reconcileNativeString(rawFields, "sni", fields.serverName, options, "server_name")
	reconcileNativeBool(rawFields, "insecure", fields.insecure, options, "insecure")
	if fields.insecure != nil && rawConfigHasField(rawFields, "insecure_skip_verify") {
		reconcileNativeBool(rawFields, "insecure_skip_verify", fields.insecure, options, "insecure")
	}
	reconcileNativeBool(rawFields, "disable_sni", fields.disableSNI, options, "disable_sni")

	if fields.alpnString != nil && rawConfigHasField(rawFields, "alpn") {
		values := splitCompatibilityList(*fields.alpnString)
		if len(values) == 0 {
			delete(options, "alpn")
		} else {
			options["alpn"] = values
		}
	}
	if fields.alpnList != nil && rawConfigHasField(rawFields, "alpn") {
		values := append([]string(nil), (*fields.alpnList)...)
		if len(values) == 0 {
			delete(options, "alpn")
		} else {
			options["alpn"] = values
		}
	}

	if fields.fingerprint != nil && rawConfigHasField(rawFields, "fingerprint") {
		fingerprint := strings.TrimSpace(*fields.fingerprint)
		if fingerprint == "" || strings.EqualFold(fingerprint, "none") {
			delete(options, "utls")
		} else {
			utls := nativeCompatibilityMap(options["utls"])
			if utls == nil {
				utls = map[string]interface{}{}
			}
			utls["enabled"] = true
			utls["fingerprint"] = fingerprint
			options["utls"] = utls
		}
	}

	if !realityCompatible {
		if fields.publicKey != nil {
			*fields.publicKey = ""
		}
		if fields.shortID != nil {
			*fields.shortID = ""
		}
		delete(options, "reality")
	} else if fields.publicKey != nil || fields.shortID != nil {
		reality := nativeCompatibilityMap(options["reality"])
		if reality == nil {
			reality = map[string]interface{}{}
		}
		if fields.publicKey != nil && rawConfigHasField(rawFields, "public_key") {
			setOrDeleteNativeString(reality, "public_key", *fields.publicKey)
		}
		if fields.shortID != nil && rawConfigHasField(rawFields, "short_id") {
			setOrDeleteNativeString(reality, "short_id", *fields.shortID)
		}
		if len(reality) == 0 {
			delete(options, "reality")
		} else {
			options["reality"] = reality
		}
	}
}

func reconcileNativeTransportCompatibility(rawFields map[string]json.RawMessage, fields nativeTransportCompatibilityFields) {
	if fields.options == nil || fields.network == nil || len(*fields.options) == 0 {
		return
	}
	options := *fields.options
	network := normalizeCompatibilityTransportType(*fields.network)
	if rawConfigHasField(rawFields, "network") {
		if network == "" {
			*fields.options = nil
			return
		}
		options["type"] = network
	} else if network == "" {
		network = normalizeCompatibilityTransportType(nativeCompatibilityString(options["type"]))
	}

	switch network {
	case "ws":
		reconcileNativeString(rawFields, "path", fields.path, options, "path")
		reconcileNativeHeaders(rawFields, fields.headers, options)
		reconcileNativeWebSocketHost(rawFields, fields.host, options)
		reconcileNativeInt(rawFields, "max_early_data", fields.maxEarlyData, options, "max_early_data")
		reconcileNativeString(rawFields, "early_data_header", fields.earlyDataName, options, "early_data_header_name")
	case "grpc":
		reconcileNativeString(rawFields, "service_name", fields.serviceName, options, "service_name")
	case "http":
		if fields.httpPaths != nil && rawConfigHasField(rawFields, "http_path") {
			if len(*fields.httpPaths) == 0 {
				delete(options, "path")
			} else {
				options["path"] = (*fields.httpPaths)[0]
			}
		} else {
			reconcileNativeString(rawFields, "path", fields.path, options, "path")
		}
		if fields.host != nil && rawConfigHasField(rawFields, "host") {
			hosts := splitCompatibilityList(*fields.host)
			if len(hosts) == 0 {
				delete(options, "host")
			} else {
				options["host"] = hosts
			}
		}
		reconcileNativeString(rawFields, "method", fields.method, options, "method")
		reconcileNativeHeaders(rawFields, fields.headers, options)
	case "httpupgrade":
		if fields.upgradePath != nil && rawConfigHasField(rawFields, "http_upgrade_path") {
			reconcileNativeString(rawFields, "http_upgrade_path", fields.upgradePath, options, "path")
		} else {
			reconcileNativeString(rawFields, "path", fields.path, options, "path")
		}
		if fields.upgradeHost != nil && rawConfigHasField(rawFields, "http_upgrade_host") {
			reconcileNativeString(rawFields, "http_upgrade_host", fields.upgradeHost, options, "host")
		} else {
			reconcileNativeString(rawFields, "host", fields.host, options, "host")
		}
		reconcileNativeHeaders(rawFields, fields.headers, options)
	}
}

func reconcileNativeString(rawFields map[string]json.RawMessage, rawKey string, value *string, options map[string]interface{}, nativeKey string) {
	if value == nil || !rawConfigHasField(rawFields, rawKey) {
		return
	}
	setOrDeleteNativeString(options, nativeKey, *value)
}

func reconcileNativeBool(rawFields map[string]json.RawMessage, rawKey string, value *bool, options map[string]interface{}, nativeKey string) {
	if value == nil || !rawConfigHasField(rawFields, rawKey) {
		return
	}
	if *value {
		options[nativeKey] = true
	} else {
		delete(options, nativeKey)
	}
}

func reconcileNativeInt(rawFields map[string]json.RawMessage, rawKey string, value *int, options map[string]interface{}, nativeKey string) {
	if value == nil || !rawConfigHasField(rawFields, rawKey) {
		return
	}
	if *value > 0 {
		options[nativeKey] = *value
	} else {
		delete(options, nativeKey)
	}
}

func reconcileNativeHeaders(rawFields map[string]json.RawMessage, headers *map[string]string, options map[string]interface{}) {
	if headers == nil || !rawConfigHasField(rawFields, "headers") {
		return
	}
	if len(*headers) == 0 {
		delete(options, "headers")
		return
	}
	nativeHeaders := make(map[string]interface{}, len(*headers))
	for key, value := range *headers {
		nativeHeaders[key] = value
	}
	options["headers"] = nativeHeaders
}

func reconcileNativeWebSocketHost(rawFields map[string]json.RawMessage, host *string, options map[string]interface{}) {
	if host == nil || !rawConfigHasField(rawFields, "host") {
		return
	}
	headers := nativeCompatibilityMap(options["headers"])
	if headers == nil {
		headers = map[string]interface{}{}
	}
	for key := range headers {
		if strings.EqualFold(key, "host") {
			delete(headers, key)
		}
	}
	if value := strings.TrimSpace(*host); value != "" {
		headers["Host"] = *host
	}
	if len(headers) == 0 {
		delete(options, "headers")
	} else {
		options["headers"] = headers
	}
}

func setOrDeleteNativeString(options map[string]interface{}, key string, value string) {
	if strings.TrimSpace(value) == "" {
		delete(options, key)
	} else {
		options[key] = value
	}
}

func rawConfigHasField(rawFields map[string]json.RawMessage, key string) bool {
	_, exists := rawFields[key]
	return exists
}

func nativeCompatibilityMap(value interface{}) map[string]interface{} {
	switch typed := value.(type) {
	case NativeOptions:
		return map[string]interface{}(typed)
	case map[string]interface{}:
		return typed
	default:
		return nil
	}
}

func nativeCompatibilityString(value interface{}) string {
	text, _ := value.(string)
	return text
}

func normalizeCompatibilityTransportType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "tcp", "none", "raw":
		return ""
	case "h2":
		return "http"
	case "gun":
		return "grpc"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func splitCompatibilityList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
