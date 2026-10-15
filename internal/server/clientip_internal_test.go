package server

import (
	"testing"

	"shepherd/internal/config"
)

// TestPeerTrusted pins how the TCP peer in r.RemoteAddr is matched against
// server.trusted_proxies, including the address spellings a dual-stack
// listener or a link-local connection produces.
func TestPeerTrusted(t *testing.T) {
	prefixes, err := config.ParseTrustedProxies([]string{"10.244.0.0/16", "fd00::/16", "fe80::/16"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		remoteAddr string
		want       bool
	}{
		{"10.244.1.7:51234", true},
		{"10.245.1.7:51234", false},
		// IPv4 client on a dual-stack listener: folds to v4 before the check.
		{"[::ffff:10.244.1.7]:51234", true},
		{"[::ffff:10.245.1.7]:51234", false},
		{"[fd00::5]:51234", true},
		{"[fd01::5]:51234", false},
		// A zone never defeats (or satisfies) the check on its own.
		{"[fe80::1%eth0]:51234", true},
		{"[fe81::1%eth0]:51234", false},
		// Bare addresses (tests, unusual listeners).
		{"10.244.1.7", true},
		{"192.0.2.1", false},
		// Unparseable peers are never trusted.
		{"", false},
		{"not-an-ip:80", false},
		{"@", false},
	} {
		if got := peerTrusted(tc.remoteAddr, prefixes); got != tc.want {
			t.Errorf("peerTrusted(%q) = %v, want %v", tc.remoteAddr, got, tc.want)
		}
	}

	if peerTrusted("10.244.1.7:51234", nil) {
		t.Error("an empty trusted list must trust no peer")
	}
}
