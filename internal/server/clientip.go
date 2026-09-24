package server

import (
	"net"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5/middleware"
)

// clientIP resolves the real client IP (read with middleware.GetClientIP) for audit logs
// and rate limiting.
//
// X-Forwarded-For is consulted only when the TCP peer is itself one of trustedProxies.
// chi's ClientIPFromXFF alone does not check the peer, so a client reaching the API
// directly (bypassing the proxy) could otherwise spoof its IP with a forged header.
func clientIP(trustedProxies []string) func(http.Handler) http.Handler {
	if len(trustedProxies) == 0 {
		return middleware.ClientIPFromRemoteAddr
	}
	prefixes := make([]netip.Prefix, 0, len(trustedProxies))
	for _, p := range trustedProxies {
		prefixes = append(prefixes, netip.MustParsePrefix(p)) // validated by config.Validate
	}
	return func(next http.Handler) http.Handler {
		viaProxy := middleware.ClientIPFromXFF(trustedProxies...)(next)
		direct := middleware.ClientIPFromRemoteAddr(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if peerIsTrusted(r.RemoteAddr, prefixes) {
				viaProxy.ServeHTTP(w, r)
				return
			}
			direct.ServeHTTP(w, r)
		})
	}
}

func peerIsTrusted(remoteAddr string, prefixes []netip.Prefix) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
