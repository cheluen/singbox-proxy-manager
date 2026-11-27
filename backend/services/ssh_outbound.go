package services

import "sb-proxy/backend/models"

func generateSSHOutbound(config *models.SSHConfig, tag string) (OutboundConfig, error) {
	if err := config.Validate(); err != nil {
		return OutboundConfig{}, err
	}
	outbound := OutboundConfig{
		Type: "ssh", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if config.User != "" {
		outbound.Extra["user"] = config.User
	}
	if config.Password != "" {
		outbound.Extra["password"] = config.Password
	}
	if len(config.PrivateKey) > 0 {
		outbound.Extra["private_key"] = append([]string(nil), config.PrivateKey...)
	}
	if config.PrivateKeyPath != "" {
		outbound.Extra["private_key_path"] = config.PrivateKeyPath
	}
	if config.PrivateKeyPassphrase != "" {
		outbound.Extra["private_key_passphrase"] = config.PrivateKeyPassphrase
	}
	if len(config.HostKey) > 0 {
		outbound.Extra["host_key"] = append([]string(nil), config.HostKey...)
	}
	if len(config.HostKeyAlgorithms) > 0 {
		outbound.Extra["host_key_algorithms"] = append([]string(nil), config.HostKeyAlgorithms...)
	}
	if config.ClientVersion != "" {
		outbound.Extra["client_version"] = config.ClientVersion
	}
	return outbound, nil
}
