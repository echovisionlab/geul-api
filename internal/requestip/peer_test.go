package requestip

import "testing"

func TestHostFromPeerAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
		want string
	}{
		{name: "ipv4 with port", addr: "203.0.113.5:443", want: "203.0.113.5"},
		{name: "ipv4 without port", addr: "203.0.113.5", want: "203.0.113.5"},
		{name: "bracketed ipv6 with port", addr: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "bare ipv6", addr: "2001:db8::1", want: "2001:db8::1"},
		{name: "bracketed ipv6 without port", addr: "[2001:db8::1]", want: "2001:db8::1"},
		{name: "dns host with port", addr: "example.test:443", want: "example.test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := HostFromPeerAddr(tt.addr); got != tt.want {
				t.Fatalf("HostFromPeerAddr(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestTrustedClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		xff      string
		realIP   string
		peerAddr string
		want     string
	}{
		{
			name: "uses original x-forwarded-for client hop",
			xff:  "198.51.100.10, 203.0.113.5",
			want: "198.51.100.10",
		},
		{
			name:   "falls back to x-real-ip",
			realIP: "203.0.113.7",
			want:   "203.0.113.7",
		},
		{
			name:     "falls back to peer address",
			peerAddr: "203.0.113.8:443",
			want:     "203.0.113.8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := TrustedClientIP(tt.xff, tt.realIP, tt.peerAddr); got != tt.want {
				t.Fatalf("TrustedClientIP(%q, %q, %q) = %q, want %q", tt.xff, tt.realIP, tt.peerAddr, got, tt.want)
			}
		})
	}
}
