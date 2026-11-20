package config

import (
	"os"
	"strings"
)

const (
	keyServerName   = "serverName"
	keyHostPublicIP = "hostPublicIP"

	unknownHostDisplayName = "unknown-host"
)

// HostDisplayName resolves the label used in outbound alerts:
// serverName → hostPublicIP → OS hostname → "unknown-host".
func (c *HostConfig) HostDisplayName() string {
	if c == nil {
		return ResolveHostDisplayName("", "", os.Hostname)
	}

	return ResolveHostDisplayName(c.ServerName, c.HostPublicIP, os.Hostname)
}

// ResolveHostDisplayName applies the serverName → hostPublicIP → hostname chain.
// hostname is injectable for tests.
func ResolveHostDisplayName(serverName, hostPublicIP string, hostname func() (string, error)) string {
	if value := strings.TrimSpace(serverName); value != "" {
		return value
	}

	if value := strings.TrimSpace(hostPublicIP); value != "" {
		return value
	}

	if hostname == nil {
		return unknownHostDisplayName
	}

	name, err := hostname()
	if err != nil {
		return unknownHostDisplayName
	}

	if value := strings.TrimSpace(name); value != "" {
		return value
	}

	return unknownHostDisplayName
}
