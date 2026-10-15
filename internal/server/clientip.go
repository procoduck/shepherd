package server

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5/middleware"

	"shepherd/internal/config"
)

// clientIPMiddleware records the request's client address in the context
// (read it with middleware.GetClientIP; internal/auth's clientIP does). It
// never rewrites r.RemoteAddr — that stays the TCP peer for everything else.
//
// The rule, in full:
//
//   - If the TCP peer is NOT inside server.trusted_proxies (always the case
//     when the list is empty, the default), the client is the TCP peer.
//     Forwarding headers are ignored: anyone can send them.
//   - If the TCP peer IS a trusted proxy, the client is the rightmost
//     X-Forwarded-For entry that is not itself trusted (chi's
//     ClientIPFromXFF: entries a client prepends sit to the LEFT of what its
//     proxies append, so they are never reached). If that yields nothing —
//     no header, every entry trusted, or an unparseable entry (fail-closed) —
//     the client falls back to the TCP peer. An entry carrying a port
//     ("203.0.113.5:4711") does not parse as an address, so it too falls
//     back to the peer (the shared bucket), never to a client-chosen key.
//   - X-Real-IP and True-Client-IP are never read: they pass a
//     client-supplied value through unless the edge strips it.
//     X-Forwarded-For is what both supported proxies maintain: ingress-nginx
//     by default SETS it to the address it saw (`$remote_addr`, replacing
//     whatever the client sent), NGINX Gateway Fabric APPENDS that address
//     (`$proxy_add_x_forwarded_for`). Either way the rightmost entry is the
//     proxy's own observation. That stops being true if the proxy itself is
//     told to trust client headers — ingress-nginx's use-forwarded-headers /
//     enable-real-ip / use-proxy-protocol with the default
//     proxy-real-ip-cidr of 0.0.0.0/0, or NGF's NginxProxy rewriteClientIP
//     with loose trustedAddresses — because $remote_addr then becomes
//     client-chosen before it reaches us. Nothing here can detect that; the
//     docs (configuration.html#trusted-proxies) warn operators off it.
//
// This replaces chi's middleware.RealIP, which trusted all three headers from
// any peer and mutated r.RemoteAddr (GHSA-3fxj-6jh8-hvhx).
func clientIPMiddleware(trustedProxies []string, logger *slog.Logger) func(http.Handler) http.Handler {
	prefixes, err := config.ParseTrustedProxies(trustedProxies)
	if err != nil {
		// config.Load already rejected this, so only a hand-built Config gets
		// here. Fail closed: trust nobody.
		logger.Error("ignoring invalid server.trusted_proxies; forwarding headers will not be trusted", "err", err)
		prefixes = nil
	}
	cidrs := make([]string, len(prefixes))
	for i, p := range prefixes {
		cidrs[i] = p.String()
	}

	return func(next http.Handler) http.Handler {
		fromPeer := middleware.ClientIPFromRemoteAddr(next)
		if len(prefixes) == 0 {
			return fromPeer
		}
		fromXFF := middleware.ClientIPFromXFF(cidrs...)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if middleware.GetClientIPAddr(r.Context()).IsValid() {
				next.ServeHTTP(w, r)
				return
			}
			fromPeer.ServeHTTP(w, r)
		}))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if peerTrusted(r.RemoteAddr, prefixes) {
				fromXFF.ServeHTTP(w, r)
				return
			}
			fromPeer.ServeHTTP(w, r)
		})
	}
}

// peerTrusted reports whether the TCP peer in remoteAddr ("ip:port", or a
// bare IP) is inside one of prefixes. v4-mapped IPv6 folds to v4 and any zone
// is dropped, matching how ParseTrustedProxies normalised the prefixes.
func peerTrusted(remoteAddr string, prefixes []netip.Prefix) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap().WithZone("")
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
