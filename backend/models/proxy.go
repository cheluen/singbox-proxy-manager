package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ProxyNode represents a proxy node configuration
type ProxyNode struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Remark      string    `json:"remark"`
	Type        string    `json:"type"`   // ss, vless, vmess, hy2, tuic, trojan, anytls, socks5, http
	Config      string    `json:"config"` // JSON string of protocol-specific config
	InboundPort int       `json:"inbound_port"`
	Username    string    `json:"username"`
	Password    string    `json:"password"`
	SortOrder   int       `json:"sort_order"`
	NodeIP      string    `json:"node_ip"`
	Location    string    `json:"location"`
	CountryCode string    `json:"country_code"`
	Latency     int       `json:"latency"` // in milliseconds
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SSConfig represents Shadowsocks configuration
type SSConfig struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Method     string `json:"method"`
	Password   string `json:"password"`
	Plugin     string `json:"plugin,omitempty"`
	PluginOpts string `json:"plugin_opts,omitempty"`
	// Additional parameters
	UDPOverTCP      bool                   `json:"udp_over_tcp,omitempty"`
	MultiplexConfig map[string]interface{} `json:"multiplex,omitempty"`
}

// VLESSConfig represents VLESS configuration
type VLESSConfig struct {
	Server      string `json:"server"`
	ServerPort  int    `json:"server_port"`
	UUID        string `json:"uuid"`
	Flow        string `json:"flow,omitempty"`
	Encryption  string `json:"encryption,omitempty"`
	Network     string `json:"network,omitempty"`  // tcp, kcp, ws, http, quic, grpc, httpupgrade
	Security    string `json:"security,omitempty"` // none, tls, reality
	SNI         string `json:"sni,omitempty"`
	ALPN        string `json:"alpn,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	PublicKey   string `json:"public_key,omitempty"`
	ShortID     string `json:"short_id,omitempty"`
	SpiderX     string `json:"spider_x,omitempty"`
	Insecure    bool   `json:"insecure,omitempty"`
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
	MultiplexConfig map[string]interface{} `json:"multiplex,omitempty"`
}

// VMESSConfig represents VMess configuration
type VMESSConfig struct {
	Server      string `json:"server"`
	ServerPort  int    `json:"server_port"`
	UUID        string `json:"uuid"`
	AlterID     int    `json:"alter_id"`
	Security    string `json:"security,omitempty"` // auto, aes-128-gcm, chacha20-poly1305, none, zero
	Network     string `json:"network,omitempty"`  // tcp, kcp, ws, http, quic, grpc, httpupgrade
	TLS         string `json:"tls,omitempty"`      // none, tls
	SNI         string `json:"sni,omitempty"`
	ALPN        string `json:"alpn,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Insecure    bool   `json:"insecure,omitempty"`
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
	MultiplexConfig map[string]interface{} `json:"multiplex,omitempty"`
}

// Hysteria2Config represents Hysteria2 configuration
type Hysteria2Config struct {
	Server             string   `json:"server"`
	ServerPort         int      `json:"server_port"`
	Password           string   `json:"password"`
	UpMbps             int      `json:"up_mbps,omitempty"`
	DownMbps           int      `json:"down_mbps,omitempty"`
	Obfs               string   `json:"obfs,omitempty"`
	ObfsPassword       string   `json:"obfs_password,omitempty"`
	SNI                string   `json:"sni,omitempty"`
	ALPN               []string `json:"alpn,omitempty"`
	Fingerprint        string   `json:"fingerprint,omitempty"`
	InsecureSkipVerify bool     `json:"insecure_skip_verify,omitempty"`
	// Additional Hysteria2 parameters
	SalamanderPassword string `json:"salamander_password,omitempty"`
	BrutalDownMbps     int    `json:"brutal_down_mbps,omitempty"`
	BrutalUpMbps       int    `json:"brutal_up_mbps,omitempty"`
	Network            string `json:"network,omitempty"` // tcp or udp
	HopInterval        string `json:"hop_interval,omitempty"`
}

// TrojanConfig represents Trojan configuration
type TrojanConfig struct {
	Server          string                 `json:"server"`
	ServerPort      int                    `json:"server_port"`
	Password        string                 `json:"password"`
	Network         string                 `json:"network,omitempty"` // tcp, ws, grpc, http, httpupgrade
	SNI             string                 `json:"sni,omitempty"`
	ALPN            []string               `json:"alpn,omitempty"`
	Fingerprint     string                 `json:"fingerprint,omitempty"`
	Insecure        bool                   `json:"insecure,omitempty"`
	Host            string                 `json:"host,omitempty"`         // ws/http Host header
	Path            string                 `json:"path,omitempty"`         // ws/http path
	ServiceName     string                 `json:"service_name,omitempty"` // grpc service name
	HTTPMethod      string                 `json:"method,omitempty"`       // http/h2 method
	Headers         map[string]string      `json:"headers,omitempty"`      // transport headers
	MultiplexConfig map[string]interface{} `json:"multiplex,omitempty"`
}

// TUICConfig represents TUIC configuration
type TUICConfig struct {
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
	Heartbeat          string   `json:"heartbeat,omitempty"`
	// Additional TUIC parameters
	Network    string `json:"network,omitempty"` // tcp or udp
	DisableSNI bool   `json:"disable_sni,omitempty"`
	ReduceRTT  bool   `json:"reduce_rtt,omitempty"`
}

// AnyTLSConfig represents AnyTLS configuration
type AnyTLSConfig struct {
	Server                   string   `json:"server"`
	ServerPort               int      `json:"server_port"`
	Password                 string   `json:"password"`
	SNI                      string   `json:"sni,omitempty"`
	ALPN                     []string `json:"alpn,omitempty"`
	Fingerprint              string   `json:"fingerprint,omitempty"`
	Insecure                 bool     `json:"insecure,omitempty"`
	IdleSessionCheckInterval string   `json:"idle_session_check_interval,omitempty"`
	IdleSessionTimeout       string   `json:"idle_session_timeout,omitempty"`
	MinIdleSession           int      `json:"min_idle_session,omitempty"`
}

// SOCKS5Config represents SOCKS5 proxy configuration
type SOCKS5Config struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
}

// HTTPProxyConfig represents HTTP/HTTPS proxy configuration
type HTTPProxyConfig struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	TLS        bool   `json:"tls,omitempty"`
	Insecure   bool   `json:"insecure,omitempty"`
	SNI        string `json:"sni,omitempty"`
}

// Settings represents global settings
type Settings struct {
	ID            int       `json:"id"`
	AdminPassword string    `json:"admin_password"`
	StartPort     int       `json:"start_port"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// InitDB initializes the database
func InitDB(db *sql.DB) error {
	// Create proxy_nodes table
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS proxy_nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			remark TEXT DEFAULT '',
			type TEXT NOT NULL,
			config TEXT NOT NULL,
			inbound_port INTEGER NOT NULL,
			username TEXT DEFAULT '',
			password TEXT DEFAULT '',
			sort_order INTEGER NOT NULL,
			node_ip TEXT DEFAULT '',
			location TEXT DEFAULT '',
			country_code TEXT DEFAULT '',
			latency INTEGER DEFAULT 0,
			enabled INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}

	if err := ensureColumn(db, "proxy_nodes", "remark", "ALTER TABLE proxy_nodes ADD COLUMN remark TEXT DEFAULT ''"); err != nil {
		return err
	}

	// Create settings table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS settings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			admin_password TEXT NOT NULL,
			start_port INTEGER DEFAULT 10000,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}

	// Insert default settings if not exists
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM settings").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		// Get initial password from environment variable or use default
		initialPassword := os.Getenv("ADMIN_PASSWORD")
		if initialPassword == "" {
			initialPassword = "admin123"
		}

		// Hash the initial password with bcrypt
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(initialPassword), bcrypt.DefaultCost)
		if err != nil {
			log.Printf("Failed to hash initial password: %v", err)
			return err
		}

		_, err = db.Exec("INSERT INTO settings (admin_password, start_port) VALUES (?, ?)", string(hashedPassword), 30001)
		if err != nil {
			return err
		}
		log.Println("Initial admin password has been set and hashed")
	}

	return nil
}

func ensureColumn(db *sql.DB, table string, column string, alterSQL string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
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
			return err
		}
		if name == column {
			return nil
		}
	}

	_, err = db.Exec(alterSQL)
	return err
}

// ParseConfig parses the config string based on proxy type
func (p *ProxyNode) ParseConfig() (interface{}, error) {
	var config interface{}
	switch p.Type {
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
	case "socks5":
		config = &SOCKS5Config{}
	case "http":
		config = &HTTPProxyConfig{}
	default:
		return nil, nil
	}

	if err := json.Unmarshal([]byte(p.Config), config); err != nil {
		return nil, err
	}
	return config, nil
}
