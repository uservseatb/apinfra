package docker

import "time"

const (
	defaultLoginTimeout       = time.Minute
	defaultComposePullTimeout = 10 * time.Minute
	defaultComposeUpTimeout   = 5 * time.Minute
	defaultComposeDownTimeout = 2 * time.Minute
	defaultPruneTimeout       = 2 * time.Minute
	defaultAPITimeout         = 30 * time.Second
	defaultProxyReadyAfter    = 30 * time.Second
)

func (t ApplyTimeouts) withDefaults() ApplyTimeouts {
	out := t
	if out.Login <= 0 {
		out.Login = defaultLoginTimeout
	}

	if out.ComposePull <= 0 {
		out.ComposePull = defaultComposePullTimeout
	}

	if out.ComposeUp <= 0 {
		out.ComposeUp = defaultComposeUpTimeout
	}

	if out.ComposeDown <= 0 {
		out.ComposeDown = defaultComposeDownTimeout
	}

	if out.Prune <= 0 {
		out.Prune = defaultPruneTimeout
	}

	if out.API <= 0 {
		out.API = defaultAPITimeout
	}

	if out.ProxyReadyAfter <= 0 {
		out.ProxyReadyAfter = defaultProxyReadyAfter
	}

	return out
}
