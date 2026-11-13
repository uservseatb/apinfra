package sys

import (
	"net/url"
	"regexp"
	"strings"
)

const redactedSecret = "***"

// URL-like spans: scheme://… stopping before whitespace, quotes, brackets.
var urlCandidateRE = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>\[\]()]+`)

// Bare PATs / cloud keys left in dial/DNS text after URL masking.
var bareCredentialTokenRE = regexp.MustCompile(
	`(?i)\b(?:ghp_|gho_|ghu_|ghs_|ghr_|github_pat_|glpat-|glrt-|dckr_pat_)[A-Za-z0-9_\-]+` +
		`|\bAKIA[A-Z0-9]{16}\b` +
		`|(?i)\bBearer\s+[A-Za-z0-9._\-]+`,
)

var (
	// --password-stdin is intentionally not matched (no '=' / space after the flag name).
	cliLongFlagAssignRE = regexp.MustCompile(`(?i)(--(?:password|passwd|token)=)\S+`)
	cliLongFlagSpaceRE  = regexp.MustCompile(`(?i)(--(?:password|passwd|token)\s+)\S+`)
	cliShortPasswordRE  = regexp.MustCompile(`(?i)(^|\s)(-p\s+)\S+`)
	cliEnvAssignRE      = regexp.MustCompile(`(?i)\b([A-Za-z_]*(?:PASSWORD|TOKEN|SECRET|PASSWD)=)\S+`)
)

// RedactURLUserinfo hides credentials in URLs, bare PATs, and common CLI secret
// flags anywhere in s (logs, argv joins, error text).
func RedactURLUserinfo(input string) string {
	return redactSecrets(input)
}

func redactSecrets(input string) string {
	out := redactURLs(input)
	out = redactCLISecrets(out)

	return bareCredentialTokenRE.ReplaceAllString(out, redactedSecret)
}

func redactURLs(input string) string {
	return urlCandidateRE.ReplaceAllStringFunc(input, func(match string) string {
		core, trail := splitURLTrail(match)

		return redactOneURL(core) + trail
	})
}

func splitURLTrail(match string) (string, string) {
	i := len(match)
	for i > 0 {
		switch match[i-1] {
		case '.', ',', ';', ':', '!', '?', ')', ']', '\'', '"':
			i--
		default:
			return match[:i], match[i:]
		}
	}

	return match, ""
}

func redactOneURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return redactURLFallback(raw)
	}

	host := parsed.Hostname()
	if host == "" && parsed.Host == "" && parsed.Opaque == "" {
		return redactURLFallback(raw)
	}

	if patShapedHost(host) {
		return maskAfterScheme(raw)
	}

	if parsed.User != nil {
		return maskUserinfo(parsed)
	}

	return raw
}

// maskUserinfo keeps scheme/host/path/query and replaces userinfo with ***
// without going through url.User.String() (which percent-encodes '*').
func maskUserinfo(parsed *url.URL) string {
	cleared := *parsed
	cleared.User = nil
	plain := cleared.String()
	prefix := parsed.Scheme + "://"

	if !strings.HasPrefix(plain, prefix) {
		return redactURLFallback(parsed.String())
	}

	return prefix + redactedSecret + "@" + strings.TrimPrefix(plain, prefix)
}

func redactURLFallback(raw string) string {
	schemeIdx := strings.Index(raw, "://")
	if schemeIdx < 0 {
		return raw
	}

	rest := raw[schemeIdx+len("://"):]
	endAuth := len(rest)

	for _, sep := range []byte{'/', '?', '#'} {
		if j := strings.IndexByte(rest, sep); j >= 0 && j < endAuth {
			endAuth = j
		}
	}

	auth := rest[:endAuth]
	if at := strings.LastIndexByte(auth, '@'); at >= 0 {
		return raw[:schemeIdx+len("://")] + redactedSecret + "@" + rest[at+1:]
	}

	host := auth
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}

	if patShapedHost(host) {
		return maskAfterScheme(raw)
	}

	return raw
}

func maskAfterScheme(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		return raw[:i+len("://")] + redactedSecret
	}

	return redactedSecret
}

func patShapedHost(host string) bool {
	if host == "" {
		return false
	}

	lower := strings.ToLower(host)
	for _, prefix := range []string{
		"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-", "glrt-", "dckr_pat_",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	upper := strings.ToUpper(host)
	if strings.HasPrefix(upper, "AKIA") && len(host) >= 20 {
		return true
	}

	return false
}

func redactCLISecrets(input string) string {
	out := cliLongFlagAssignRE.ReplaceAllString(input, "${1}"+redactedSecret)
	out = cliLongFlagSpaceRE.ReplaceAllString(out, "${1}"+redactedSecret)
	out = cliShortPasswordRE.ReplaceAllString(out, "${1}${2}"+redactedSecret)
	out = cliEnvAssignRE.ReplaceAllString(out, "${1}"+redactedSecret)

	return out
}
