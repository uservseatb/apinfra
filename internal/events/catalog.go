package events

// Production event types written to ~/.events (JSONL "type" field).
const (
	GitLoginFailed       = "git_login_failed"
	DockerLoginFailed    = "docker_login_failed"
	SecurityViolation    = "security_violation"
	TrustedImageDeployed = "trusted_image_deployed"
	CertRateLimited      = "cert_rate_limited"
	CertIssueFailed      = "cert_issue_failed"
)

// Event field keys (string values in the JSONL object).
const (
	FieldRepo         = "repo"
	FieldRegistry     = "registry"
	FieldProject      = "project"
	FieldService      = "service"
	FieldSource       = "source"
	FieldReason       = "reason"
	FieldImage        = "image"
	FieldDigest       = "digest"
	FieldDomain       = "domain"
	FieldFrom         = "from"
	FieldTo           = "to"
	FieldAttempts     = "attempts"
	FieldMode         = "mode"
	FieldBlockedUntil = "blocked_until"
)

// SecurityViolation reason values.
const (
	ReasonDisallowedBindMount        = "disallowed_bind_mount"
	ReasonUntrustedDockerSocketMount = "untrusted_docker_socket_mount"
	ReasonDisallowedVolumeType       = "disallowed_volume_type"
)
