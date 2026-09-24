// Package safehttp builds HTTP clients for requests to user-supplied URLs (self-hosted Git
// instances, and later uptime checks and webhook channels). It defends against SSRF:
//
//   - the destination is checked after DNS resolution, when the connection is dialed, so a
//     hostname that resolves to an internal address (or re-resolves to one: DNS rebinding) is
//     refused;
//   - loopback, private, link-local, CGNAT, multicast and unspecified addresses are blocked
//     unless an operator allow-lists them (OPSHUB_OUTBOUND_ALLOWED_CIDRS);
//   - redirects are not followed and environment proxies are ignored;
//   - requests time out and response bodies are size-limited by callers (ReadLimited).
package safehttp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// ErrBlocked is returned (wrapped) when the destination address is not allowed.
var ErrBlocked = errors.New("destination address is not allowed")

// Options configure a client.
type Options struct {
	// AllowedCIDRs are exempt from the internal-address block (e.g. a self-hosted GitLab on a
	// private network).
	AllowedCIDRs []netip.Prefix
	// Timeout bounds the whole request, including reading the body. Default 15 s.
	Timeout time.Duration
}

// blocked lists ranges that are never reachable unless allow-listed. netip's IsPrivate,
// IsLoopback, IsLinkLocalUnicast (incl. 169.254.169.254 cloud metadata) etc. cover most;
// these add the ranges they don't.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 (can reach IPv4 internals)
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2001:db8::/32"),  // documentation
	netip.MustParsePrefix("fec0::/10"),      // deprecated site-local
	netip.MustParsePrefix("::ffff:0:0/96"),  // IPv4-mapped (checked after unmapping anyway)
	netip.MustParsePrefix("100::/64"),       // discard-only
	netip.MustParsePrefix("2002::/16"),      // 6to4 (can embed internal IPv4)
	netip.MustParsePrefix("2001::/32"),      // Teredo
}

// Allowed reports whether a client with these options may connect to addr.
func (o Options) Allowed(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range o.AllowedCIDRs {
		if p.Contains(addr) {
			return true
		}
	}
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() ||
		addr.IsUnspecified() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// NewClient returns an http.Client that enforces the options.
func NewClient(o Options) *http.Client {
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Second
	}
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		// Control runs for every address the dialer tries, after DNS resolution.
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrBlocked, address)
			}
			if !o.Allowed(ap.Addr()) {
				return fmt.Errorf("%w: %s", ErrBlocked, ap.Addr())
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:                 nil, // a proxy would hide the real destination from the check
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   o.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ParseCIDRs parses a list of CIDRs or single addresses (config values).
func ParseCIDRs(values []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !strings.Contains(v, "/") {
			a, err := netip.ParseAddr(v)
			if err != nil {
				return nil, fmt.Errorf("invalid address %q: %w", v, err)
			}
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", v, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// IsBlocked reports whether err (from a client built here) was caused by the address block.
func IsBlocked(err error) bool { return errors.Is(err, ErrBlocked) }

// ReadLimited reads at most limit bytes of a response body and reports whether it was cut.
func ReadLimited(r io.Reader, limit int64) ([]byte, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > limit {
		return b[:limit], true, nil
	}
	return b, false, nil
}
