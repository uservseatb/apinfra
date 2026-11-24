package config

import (
	"strings"
)

// fileReader is the config-repo filesystem API used by settings/env parsing.
// app passes *git.Repo (duck typing); config does not import git.
type fileReader interface {
	ReadFile(path string) ([]byte, error)
	Exists(path string) bool
}

// Certificate issue types for projects[].config.cert-issue-type.
const (
	certIssueMock = "mock"
	certIssueProd = "prod"
	certIssueTest = "test"
	certIssueNone = "none"
)

// Default cert-update-time-sec when not set in project config.
const (
	defaultCertUpdateSecMock = int64(2592000) // 1 month
	defaultCertUpdateSecProd = int64(5184000) // 2 months
	defaultCertUpdateSecTest = int64(2592000) // 30 days
	defaultCertUpdateSecNone = int64(0)       // unused — no certificates issued
)

// Route is one Nginx ingress for a project (from proxies[] or singular proxy).
type Route struct {
	DomainName     string
	InboundPort    string
	EntryPoint     string // proxy.target.service.name from settings
	EntryPointPort string // proxy.target.service.port
	TargetURL      string // proxy.target.url (absolute http/https; XOR with service)
	ProxyHost      string // Docker DNS host for nginx proxy_pass (service.apinfra_project)
	CertIssueType  string
	CertUpdateSec  int64
	CertAcmeEmail  string
}

// ServiceConfig describes one project from settings.yml (projects[]).
type ServiceConfig struct {
	Namespace         string
	NamespacePrefix   string
	Name              string
	Routes            []Route
	AllowPortsMapping bool // projects[].config.allow-ports-mapping (default false)
}

// StreamTarget is a Compose service referenced by stream.sni.default or routes[].
type StreamTarget struct {
	Project string
	Service string
	Port    string
}

// StreamSNIRoute is one exact SNI → Compose passthrough entry.
type StreamSNIRoute struct {
	StreamTarget

	ServerName string
}

// StreamSNI is the optional settings.yml stream.sni block.
type StreamSNI struct {
	Enabled       bool
	RejectDefault bool
	Default       StreamTarget // used when !RejectDefault and Enabled (or when default mapping is present)
	Routes        []StreamSNIRoute
}

// Settings is the validated settings.yml document for one namespace.
type Settings struct {
	Projects []ServiceConfig
	Stream   StreamSNI
}

// EnvPrefix returns the UPPERCASE project name prefix used for infra .env keys (e.g. "SCRAPIN__").
func (cfg ServiceConfig) EnvPrefix() string {
	return strings.ToUpper(cfg.Name) + "__"
}
