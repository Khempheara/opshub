package infra

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/safehttp"
	"github.com/opshub/opshub/internal/store"
)

// Probe schedule: daily after a successful check, hourly after a failed one.
const (
	checkEvery      = 24 * time.Hour
	retryFailedIn   = time.Hour
	probeTimeout    = 10 * time.Second
	probeConcurrent = 5
)

// Certificate is the API representation of a domain's certificate.
type Certificate struct {
	AssetID       uuid.UUID  `json:"asset_id"`
	AssetName     string     `json:"asset_name"`
	AssetTags     []string   `json:"asset_tags"`
	Host          string     `json:"host"`
	Port          int32      `json:"port"`
	Subject       string     `json:"subject"`
	Issuer        string     `json:"issuer"`
	DNSNames      []string   `json:"dns_names"`
	Serial        string     `json:"serial"`
	Fingerprint   string     `json:"fingerprint"`
	NotBefore     *time.Time `json:"not_before"`
	NotAfter      *time.Time `json:"not_after"`
	DaysLeft      *int       `json:"days_left"`
	Status        string     `json:"status"` // valid | expiring | expired | error
	Error         string     `json:"error"`
	LastCheckedAt time.Time  `json:"last_checked_at"`
}

func (s *Service) toCertificate(c store.SslCertificate, name string, tags []string) Certificate {
	out := Certificate{
		AssetID: c.AssetID, AssetName: name, AssetTags: tags, Host: c.Host, Port: c.Port, Subject: c.Subject, Issuer: c.Issuer,
		DNSNames: c.DnsNames, Serial: c.Serial, Fingerprint: c.Fingerprint, NotBefore: c.NotBefore, NotAfter: c.NotAfter,
		Error: c.Error, LastCheckedAt: c.LastCheckedAt, Status: s.certStatus(c.NotAfter, c.Error, true),
	}
	if out.AssetTags == nil {
		out.AssetTags = []string{}
	}
	if out.DNSNames == nil {
		out.DNSNames = []string{}
	}
	if c.NotAfter != nil {
		days := int(c.NotAfter.Sub(s.now()).Hours() / 24)
		out.DaysLeft = &days
	}
	return out
}

// ListCertificates lists the organization's domain certificates, soonest expiry first
// (infra.view). With expiringWithin > 0 only those expiring within it, or failing, are listed.
func (s *Service) ListCertificates(ctx context.Context, orgID uuid.UUID, expiringWithin time.Duration) ([]Certificate, error) {
	q := store.New(s.pool)
	if _, err := authz.Require(ctx, q, orgID, authz.InfraView); err != nil {
		return nil, err
	}
	params := store.ListCertificatesParams{OrganizationID: orgID}
	if expiringWithin > 0 {
		before := s.now().Add(expiringWithin)
		params.ExpiringBefore = &before
	}
	rows, err := q.ListCertificates(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]Certificate, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toCertificate(r.SslCertificate, r.AssetName, r.AssetTags))
	}
	return out, nil
}

// probeResult is what a TLS handshake revealed.
type probeResult struct {
	leaf *x509.Certificate
	err  string
}

// probe connects to host:port, reads the served certificate (even an invalid one) and
// verifies it for host.
func (s *Service) probe(ctx context.Context, host string, port int32) probeResult {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	d := tls.Dialer{NetDialer: s.dialer, Config: &tls.Config{
		ServerName: host, MinVersion: tls.VersionTLS12,
		InsecureSkipVerify: true, // #nosec G402 -- verified below, so invalid certificates are still reported
	}}
	conn, err := d.DialContext(ctx, "tcp", s.addr(host, port))
	if err != nil {
		if safehttp.IsBlocked(err) {
			return probeResult{err: "address not allowed (OPSHUB_OUTBOUND_ALLOWED_CIDRS)"}
		}
		return probeResult{err: "connection failed: " + shortErr(err)}
	}
	defer func() { _ = conn.Close() }()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return probeResult{err: "no certificate served"}
	}
	leaf := state.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range state.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter, Roots: s.cfg.Roots, CurrentTime: s.now()})
	res := probeResult{leaf: leaf}
	if verr != nil {
		res.err = verifyErr(verr)
	}
	return res
}

func shortErr(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}

// verifyErr turns verification errors into short, stable messages.
func verifyErr(err error) string {
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

// check probes one domain asset and stores the result.
func (s *Service) check(ctx context.Context, q *store.Queries, assetID uuid.UUID, host string, port int32) (store.SslCertificate, error) {
	res := s.probe(ctx, host, port)
	params := store.UpsertCertificateParams{
		AssetID: assetID, Host: host, Port: port, Error: res.err, DnsNames: []string{},
		NextCheckAt: s.now().Add(checkEvery),
	}
	if res.err != "" {
		params.NextCheckAt = s.now().Add(retryFailedIn)
	}
	if res.leaf != nil {
		l := res.leaf
		sum := sha256.Sum256(l.Raw)
		params.Subject, params.Issuer = l.Subject.String(), l.Issuer.String()
		params.DnsNames = l.DNSNames
		if params.DnsNames == nil {
			params.DnsNames = []string{}
		}
		params.Serial = strings.ToUpper(l.SerialNumber.Text(16))
		params.Fingerprint = hex.EncodeToString(sum[:])
		nb, na := l.NotBefore, l.NotAfter
		params.NotBefore, params.NotAfter = &nb, &na
	}
	return q.UpsertCertificate(ctx, params)
}

// CheckCertificate probes a domain asset now (infra.manage).
func (s *Service) CheckCertificate(ctx context.Context, id uuid.UUID) (Certificate, error) {
	q := store.New(s.pool)
	a, err := s.loadAsset(ctx, q, id, authz.InfraManage)
	if err != nil {
		return Certificate{}, err
	}
	if a.Kind != store.AssetKindDomain {
		return Certificate{}, apperr.BadRequest("only domains have certificates checked")
	}
	c, err := s.check(ctx, q, a.ID, a.Address, a.TlsPort)
	if err != nil {
		return Certificate{}, err
	}
	return s.toCertificate(c, a.Name, a.Tags), nil
}

// GetCertificate returns a domain's last check (infra.view); nil when never checked.
func (s *Service) GetCertificate(ctx context.Context, id uuid.UUID) (*Certificate, error) {
	q := store.New(s.pool)
	a, err := s.loadAsset(ctx, q, id, authz.InfraView)
	if err != nil {
		return nil, err
	}
	c, err := q.GetCertificate(ctx, id)
	if database.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := s.toCertificate(c, a.Name, a.Tags)
	return &out, nil
}

// CheckDue probes the domains that are due (a few at a time). It runs every 15 minutes.
func (s *Service) CheckDue(ctx context.Context) error {
	q := store.New(s.pool)
	due, err := q.DueCertificateChecks(ctx)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, probeConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for _, d := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.check(ctx, q, d.ID, d.Address, d.TlsPort); err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return first
}
