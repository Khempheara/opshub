package safehttp

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowed(t *testing.T) {
	var o Options
	for _, ip := range []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "::", "fd00::1", "fe80::1", "224.0.0.1", "::ffff:127.0.0.1",
		"::ffff:10.0.0.1", "64:ff9b::a00:1", "2002:a00:1::1", "240.0.0.1", "198.18.0.1",
	} {
		assert.False(t, o.Allowed(netip.MustParseAddr(ip)), ip)
	}
	for _, ip := range []string{"140.82.112.3", "172.65.251.78", "2606:4700::1111", "::ffff:8.8.8.8"} {
		assert.True(t, o.Allowed(netip.MustParseAddr(ip)), ip)
	}

	cidrs, err := ParseCIDRs([]string{"10.20.0.0/16", " 192.168.5.7 ", ""})
	require.NoError(t, err)
	o = Options{AllowedCIDRs: cidrs}
	assert.True(t, o.Allowed(netip.MustParseAddr("10.20.3.4")))
	assert.True(t, o.Allowed(netip.MustParseAddr("192.168.5.7")))
	assert.False(t, o.Allowed(netip.MustParseAddr("192.168.5.8")))
	assert.False(t, o.Allowed(netip.MustParseAddr("10.21.0.1")))

	_, err = ParseCIDRs([]string{"not-a-cidr"})
	assert.Error(t, err)
}

func TestClientBlocksInternalAddressesAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	// "localhost" resolves to loopback: blocked after DNS resolution, not by string matching.
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	blockedURL := "http://localhost:" + port
	resp, err := NewClient(Options{}).Get(blockedURL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	assert.True(t, IsBlocked(err), "got %v", err)

	allowed := NewClient(Options{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}})
	resp, err = allowed.Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Options{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}})
	resp, err := c.Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode, "the redirect is returned, not followed")
}

func TestReadLimited(t *testing.T) {
	b, cut, err := ReadLimited(strings.NewReader("hello world"), 5)
	require.NoError(t, err)
	assert.True(t, cut)
	assert.Equal(t, "hello", string(b))
	b, cut, err = ReadLimited(strings.NewReader("hi"), 5)
	require.NoError(t, err)
	assert.False(t, cut)
	assert.Equal(t, "hi", string(b))
}
