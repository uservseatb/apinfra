package nginx

import (
	"errors"
	"time"
)

// Certificate issue modes.
const (
	CertModeMock = "mock"
	CertModeProd = "prod"
	CertModeTest = "test"
	CertModeNone = "none"
)

const (
	defaultConfName = "_default.conf"

	defaultCertIssueTimeout      = time.Minute
	defaultNginxCLITimeout       = 30 * time.Second
	defaultDomainPrecheckTimeout = 15 * time.Second
)

// proxyContainerName is used only in domain log messages.
const proxyContainerName = "apinfra-proxy"

var (
	// ErrProxyNotReady is returned by ProxyController when the proxy container
	// cannot run nginx -t / reload yet (restarting, not running, missing).
	ErrProxyNotReady = errors.New("apinfra-proxy not ready for nginx -t/reload")
	// ErrNginxTestFailed is returned when nginx -t fails with a real config error.
	ErrNginxTestFailed = errors.New("nginx -t failed")
	// ErrNilContext is returned when ProcessRouting receives a nil context.
	ErrNilContext = errors.New("nil context")
	// ErrEmptyDomain is returned when DNS preflight gets an empty domain.
	ErrEmptyDomain = errors.New("empty domain")
	// ErrDNSPreflightFailed is returned when DNS preflight rejects a domain.
	ErrDNSPreflightFailed = errors.New("DNS preflight failed")
)

// Project is the nginx-domain view of one enabled settings.yml project.
type Project struct {
	Name           string
	Namespace      string
	DomainName     string
	InboundPort    string
	EntryPointPort string
	ProxyHost      string // Docker DNS host for proxy_pass (service.apinfra_project)
	CertIssueType  string
	CertUpdateSec  int64
	CertAcmeEmail  string
}

// CertDescriptor describes an issued certificate recorded in ~/.certs.json.
type CertDescriptor struct {
	DomainName    string
	IssueDateTime time.Time
	Mode          string
}

// ProcessTimeouts bounds ACME and host CLI used by ProcessRouting.
type ProcessTimeouts struct {
	CertIssue      time.Duration
	NginxCLI       time.Duration
	DomainPrecheck time.Duration
}

func (t ProcessTimeouts) withDefaults() ProcessTimeouts {
	out := t
	if out.CertIssue <= 0 {
		out.CertIssue = defaultCertIssueTimeout
	}

	if out.NginxCLI <= 0 {
		out.NginxCLI = defaultNginxCLITimeout
	}

	if out.DomainPrecheck <= 0 {
		out.DomainPrecheck = defaultDomainPrecheckTimeout
	}

	return out
}

// RoutingParams is the per-cycle input for ProcessRouting.
type RoutingParams struct {
	Projects     []Project
	ProxyRunning bool
	ProxyReady   bool
	ForceReload  bool
	PublicIP     string
}

// IssuedCertificate is host PEM paths returned by CertificateIssuer.
type IssuedCertificate struct {
	Fullchain string
	Privkey   string
	Mode      string
}

// ConfigSnapshot holds a conf.d file's pre-rewrite bytes for rollback.
type ConfigSnapshot struct {
	FileName string
	Existed  bool
	Data     []byte
}
