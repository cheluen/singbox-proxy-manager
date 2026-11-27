package services

import (
	"fmt"
	"net"
	"net/url"
	"strconv"

	"sb-proxy/backend/models"
)

func parseSSHLink(link string) (interface{}, string, string, error) {
	parsed, err := parseStandardProxyURL(link, []string{"ssh"}, 22, "SSH Node")
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid ssh link: %w", err)
	}
	params, err := url.ParseQuery(parsed.URL.RawQuery)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid ssh query encoding")
	}
	config := models.SSHConfig{
		Server:               parsed.Host,
		ServerPort:           parsed.Port,
		PrivateKey:           models.ListableString(params["private_key"]),
		PrivateKeyPath:       params.Get("private_key_path"),
		PrivateKeyPassphrase: params.Get("private_key_passphrase"),
		HostKey:              models.ListableString(params["host_key"]),
		ClientVersion:        params.Get("client_version"),
	}
	if parsed.URL.User != nil {
		config.User = parsed.URL.User.Username()
		config.Password, _ = parsed.URL.User.Password()
	}
	for _, algorithms := range params["host_key_algorithms"] {
		config.HostKeyAlgorithms = append(config.HostKeyAlgorithms, parseCommaSeparatedList(algorithms)...)
	}
	if err := applyURLDialerOptions(&config.DialerOptions, params); err != nil {
		return nil, "", "", err
	}
	return config, "ssh", parsed.Name, nil
}

func buildSSHShareLink(name string, config *models.SSHConfig) (string, error) {
	if err := config.Validate(); err != nil {
		return "", err
	}
	port := config.ServerPort
	if port == 0 {
		port = 22
	}
	link := &url.URL{
		Scheme: "ssh", Host: net.JoinHostPort(config.Server, strconv.Itoa(port)), Fragment: name,
	}
	if config.Password != "" {
		link.User = url.UserPassword(config.User, config.Password)
	} else if config.User != "" {
		link.User = url.User(config.User)
	}
	params := url.Values{}
	for key, value := range map[string]string{
		"private_key_path":       config.PrivateKeyPath,
		"private_key_passphrase": config.PrivateKeyPassphrase,
		"client_version":         config.ClientVersion,
	} {
		if value != "" {
			params.Set(key, value)
		}
	}
	for key, values := range map[string]models.ListableString{
		"private_key":         config.PrivateKey,
		"host_key":            config.HostKey,
		"host_key_algorithms": config.HostKeyAlgorithms,
	} {
		for _, value := range values {
			params.Add(key, value)
		}
	}
	if err := mergeDialerShareParams(params, config.DialerOptions); err != nil {
		return "", err
	}
	link.RawQuery = params.Encode()
	return link.String(), nil
}
