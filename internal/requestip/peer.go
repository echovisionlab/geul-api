package requestip

import (
	"net"
	"strings"
)

// HostFromPeerAddr extracts a host from net.Addr/connect.Peer strings.
// It preserves bare IPv6 addresses instead of cutting at the first colon.
func HostFromPeerAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}

	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(host, "[]")
	}

	if ip := net.ParseIP(strings.Trim(addr, "[]")); ip != nil {
		return strings.Trim(addr, "[]")
	}

	if strings.Count(addr, ":") == 1 {
		host, _, found := strings.Cut(addr, ":")
		if found && host != "" {
			return host
		}
	}

	return addr
}

// TrustedClientIP extracts a client IP from a trusted proxy chain.
// For X-Forwarded-For, the leftmost non-empty hop is the original client in
// the standard append-order format: client, proxy1, proxy2.
func TrustedClientIP(xff string, realIP string, peerAddr string) string {
	if xff != "" {
		ips := strings.SplitSeq(xff, ",")
		for candidate := range ips {
			ip := strings.TrimSpace(candidate)
			if ip != "" {
				return ip
			}
		}
	}

	if realIP = strings.TrimSpace(realIP); realIP != "" {
		return realIP
	}

	return HostFromPeerAddr(peerAddr)
}
