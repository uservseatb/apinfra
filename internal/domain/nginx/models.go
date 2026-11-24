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
	defaultConfName     = "_default.conf"
	StreamSNIFileName   = "sni.conf"
	StreamSNIIdentity   = "stream.sni"
	internalHTTPSListen = "127.0.0.1:8443"

	// SSLListenPublic is the public HTTPS listen stanza rendered when stream SNI is off.
	SSLListenPublic = "    listen 443 ssl;\n    listen [::]:443 ssl;"
	// SSLListenInternal is the loopback HTTPS listen used when stream SNI owns :443.
	SSLListenInternal = "    listen " + internalHTTPSListen + " ssl;"

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
	// ErrStreamUpstreamUnresolved is returned when a stream.sni Compose target
	// has no Docker DNS host after compose prepare.
	ErrStreamUpstreamUnresolved = errors.New("stream.sni upstream unresolved")
)

// Route is one Nginx ingress (domain or port) for a project.
type Route struct {
	DomainName     string
	InboundPort    string
	EntryPointPort string
	TargetURL      string // absolute http(s) upstream; XOR with ProxyHost+EntryPointPort
	ProxyHost      string // Docker DNS host for proxy_pass (service.apinfra_project)
	CertIssueType  string
	CertUpdateSec  int64
	CertAcmeEmail  string
}

// Project is the nginx-domain view of one enabled settings.yml project.
type Project struct {
	Name      string
	Namespace string
	Routes    []Route
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

// StreamUpstream is a resolved Compose target for stream proxy_pass.
type StreamUpstream struct {
	Project string
	Service string
	Port    string
	Host    string // Docker DNS host (service.apinfra_project); empty until resolved
}

// StreamSNIRoute is one exact SNI → Compose passthrough entry.
type StreamSNIRoute struct {
	ServerName string
	Upstream   StreamUpstream
}

// StreamSNI is the nginx-domain view of settings.yml stream.sni.
type StreamSNI struct {
	Enabled       bool
	RejectDefault bool
	Default       StreamUpstream
	Routes        []StreamSNIRoute
}

// RoutingParams is the per-cycle input for ProcessRouting.
type RoutingParams struct {
	Projects     []Project
	Stream       StreamSNI
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

// ConfigSnapshot holds a conf file's pre-rewrite bytes for rollback.
type ConfigSnapshot struct {
	FileName string
	SubDir   string // empty → conf (conf.d); "stream.d" → stream includes
	Existed  bool
	Data     []byte
}
