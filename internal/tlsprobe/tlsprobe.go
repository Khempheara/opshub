// Package tlsprobe reads the certificate a TLS server presents and verifies it for a host
// name, reporting invalid certificates with short, stable messages instead of failing the
// handshake. Used by domain assets (Module 7) and SSL monitors (Module 9).
package tlsprobe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"time"

	"github.com/opshub/opshub/internal/safehttp"
)

// Result is what a handshake revealed. Leaf is set whenever a certificate was served, even
// an invalid one; Err is empty when it is valid and trusted for the host.
type Result struct {
	Leaf *x509.Certificate
	Err  string
}

// Probe connects to addr through dialer (the SSRF guard), reads the served chain and
// verifies the leaf for host against roots (nil = system roots) at now.
func Probe(ctx context.Context, dialer *net.Dialer, addr, host string, roots *x509.CertPool, now time.Time) Result {
	d := tls.Dialer{NetDialer: dialer, Config: &tls.Config{
		ServerName: host, MinVersion: tls.VersionTLS12,
		InsecureSkipVerify: true, // #nosec G402 -- verified below, so invalid certificates are still reported
	}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		if safehttp.IsBlocked(err) {
			return Result{Err: "address not allowed (OPSHUB_OUTBOUND_ALLOWED_CIDRS)"}
		}
		return Result{Err: "connection failed: " + ShortErr(err)}
	}
	defer func() { _ = conn.Close() }()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return Result{Err: "no certificate served"}
	}
	leaf := state.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range state.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter, Roots: roots, CurrentTime: now})
	res := Result{Leaf: leaf}
	if verr != nil {
		res.Err = VerifyErr(verr)
	}
	return res
}

// ShortErr drops addresses from dial errors ("connection refused", "i/o timeout"); DNS
// failures don't reveal the resolver.
func ShortErr(err error) string {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		if dns.IsNotFound {
			return "host not found"
		}
		if dns.IsTimeout {
			return "DNS lookup timed out"
		}
		return "DNS lookup failed"
	}
	var sys *os.SyscallError
	if errors.As(err, &sys) && sys.Err != nil {
		return sys.Err.Error() // "connection refused" rather than "connect: connection refused"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}

// VerifyErr turns verification errors into short, stable messages.
func VerifyErr(err error) string {
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var unknown x509.UnknownAuthorityError
	switch {
	case errors.As(err, &hostErr):
		return "certificate is not valid for this name"
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		return "certificate expired or not yet valid"
	case errors.As(err, &unknown):
		return "certificate is not signed by a trusted authority"
	}
	return err.Error()
}
