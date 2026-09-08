package proxy

import (
	"net/url"
	"strings"
)

// isAllowedOrigin checks if the origin is in the allowed list.
// Supports:
//   - Exact match: "https://dev.example.test"
//   - Wildcard subdomain: "https://*.example.test" (matches any subdomain of example.test)
func isAllowedOrigin(origin string, allowedOrigins []string) bool {
	for _, allowed := range allowedOrigins {
		if allowed == origin || wildcardOriginMatches(origin, allowed) {
			return true
		}
	}
	return false
}

func wildcardOriginMatches(origin, allowed string) bool {
	if !strings.Contains(allowed, "://*.") {
		return false
	}
	base, err := url.Parse(strings.Replace(allowed, "*.", "", 1))
	if err != nil || base.Scheme == "" || base.Hostname() == "" {
		return false
	}
	candidate, err := url.Parse(origin)
	if err != nil || !sameOriginShape(candidate, base) {
		return false
	}
	hostname := strings.ToLower(candidate.Hostname())
	baseHostname := strings.ToLower(base.Hostname())
	subdomain := strings.TrimSuffix(hostname, "."+baseHostname)
	return subdomain != hostname && subdomain != "" && !strings.HasSuffix(subdomain, ".")
}

func sameOriginShape(candidate, base *url.URL) bool {
	return candidate.Scheme == base.Scheme && candidate.User == nil &&
		candidate.Path == "" && candidate.RawQuery == "" && candidate.Fragment == "" &&
		candidate.Port() == base.Port()
}
