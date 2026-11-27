package models

import (
	"fmt"
	"strings"
)

type SSHConfig struct {
	DialerOptions
	Server               string         `json:"server"`
	ServerPort           int            `json:"server_port"`
	User                 string         `json:"user,omitempty"`
	Password             string         `json:"password,omitempty"`
	PrivateKey           ListableString `json:"private_key,omitempty"`
	PrivateKeyPath       string         `json:"private_key_path,omitempty"`
	PrivateKeyPassphrase string         `json:"private_key_passphrase,omitempty"`
	HostKey              ListableString `json:"host_key,omitempty"`
	HostKeyAlgorithms    ListableString `json:"host_key_algorithms,omitempty"`
	ClientVersion        string         `json:"client_version,omitempty"`
}

func (config *SSHConfig) Validate() error {
	if strings.TrimSpace(config.Server) == "" {
		return fmt.Errorf("ssh server is required")
	}
	if config.ServerPort < 0 || config.ServerPort > 65535 {
		return fmt.Errorf("ssh server_port must be between 0 and 65535")
	}
	return nil
}
