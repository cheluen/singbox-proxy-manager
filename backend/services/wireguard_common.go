package services

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"sb-proxy/backend/models"
)

func firstQueryValue(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func parseWireGuardBool(raw string) (*bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		value := true
		return &value, true
	case "0", "false", "no", "off":
		value := false
		return &value, true
	default:
		return nil, false
	}
}

func parseWireGuardInt(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseWireGuardList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})

	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizeWireGuardAddress(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "/") {
		return value
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return value
	}
	if addr.Is4() {
		return value + "/32"
	}
	if addr.Is6() {
		return value + "/128"
	}
	return value
}

func normalizeWireGuardAddresses(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizeWireGuardAddress(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func collectWireGuardAddressesFromQuery(values url.Values) []string {
	var collected []string
	for _, key := range []string{"address", "local_address", "local-address", "addresses"} {
		for _, raw := range values[key] {
			collected = append(collected, parseWireGuardList(raw)...)
		}
	}

	for _, key := range []string{"ip", "ipv4"} {
		for _, raw := range values[key] {
			collected = append(collected, strings.TrimSpace(raw))
		}
	}

	for _, raw := range values["ipv6"] {
		collected = append(collected, strings.TrimSpace(raw))
	}

	return normalizeWireGuardAddresses(collected)
}

func parseWireGuardReservedString(raw string) ([]uint8, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}

	if strings.HasPrefix(value, "[") {
		var numbers []int
		if err := json.Unmarshal([]byte(value), &numbers); err != nil {
			return nil, fmt.Errorf("invalid reserved bytes: %w", err)
		}
		return intsToReservedBytes(numbers)
	}

	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(parts) > 1 {
		numbers := make([]int, 0, len(parts))
		for _, part := range parts {
			number, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return nil, fmt.Errorf("invalid reserved byte %q", part)
			}
			numbers = append(numbers, number)
		}
		return intsToReservedBytes(numbers)
	}

	trimmedHex := value
	if strings.HasPrefix(strings.ToLower(trimmedHex), "0x") {
		trimmedHex = trimmedHex[2:]
	}
	if len(trimmedHex) == 6 {
		if decoded, err := hex.DecodeString(trimmedHex); err == nil && len(decoded) == 3 {
			return []uint8{decoded[0], decoded[1], decoded[2]}, nil
		}
	}

	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		decoded, err := encoding.DecodeString(value)
		if err != nil || len(decoded) != 3 {
			continue
		}
		return []uint8{decoded[0], decoded[1], decoded[2]}, nil
	}

	return nil, fmt.Errorf("invalid reserved bytes format")
}

func parseWireGuardReservedAny(raw any) ([]uint8, error) {
	switch value := raw.(type) {
	case nil:
		return nil, nil
	case []byte:
		if len(value) == 0 {
			return nil, nil
		}
		return append([]uint8(nil), value...), nil
	case []int:
		return intsToReservedBytes(value)
	case []interface{}:
		numbers := make([]int, 0, len(value))
		for _, item := range value {
			switch typed := item.(type) {
			case int:
				numbers = append(numbers, typed)
			case int64:
				numbers = append(numbers, int(typed))
			case float64:
				numbers = append(numbers, int(typed))
			default:
				return nil, fmt.Errorf("invalid reserved byte value %T", item)
			}
		}
		return intsToReservedBytes(numbers)
	case string:
		return parseWireGuardReservedString(value)
	default:
		return nil, fmt.Errorf("unsupported reserved value %T", raw)
	}
}

func intsToReservedBytes(numbers []int) ([]uint8, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	result := make([]uint8, 0, len(numbers))
	for _, number := range numbers {
		if number < 0 || number > 255 {
			return nil, fmt.Errorf("reserved byte out of range: %d", number)
		}
		result = append(result, uint8(number))
	}
	return result, nil
}

func formatWireGuardReserved(value []uint8) string {
	if len(value) == 0 {
		return ""
	}
	parts := make([]string, 0, len(value))
	for _, item := range value {
		parts = append(parts, strconv.Itoa(int(item)))
	}
	return strings.Join(parts, ",")
}

func parseWireGuardRoutingMark(raw string) any {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(value), "0x") {
		return value
	}
	if number, err := strconv.Atoi(value); err == nil {
		return number
	}
	return value
}

func wireGuardSinglePeerFromConfig(cfg *models.WireGuardConfig) (models.WireGuardPeerConfig, bool) {
	if cfg == nil {
		return models.WireGuardPeerConfig{}, false
	}
	if len(cfg.Peers) == 1 {
		return cfg.Peers[0], true
	}
	if len(cfg.Peers) > 1 {
		return models.WireGuardPeerConfig{}, false
	}
	if strings.TrimSpace(cfg.Server) == "" || cfg.ServerPort <= 0 || strings.TrimSpace(cfg.PeerPublicKey) == "" {
		return models.WireGuardPeerConfig{}, false
	}
	return models.WireGuardPeerConfig{
		Server:       cfg.Server,
		ServerPort:   cfg.ServerPort,
		PublicKey:    cfg.PeerPublicKey,
		PreSharedKey: cfg.PreSharedKey,
		AllowedIPs:   append([]string(nil), cfg.AllowedIPs...),
		Reserved:     append([]uint8(nil), cfg.Reserved...),
	}, true
}
