package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name    string
		trusted []string
		peer    string
		xff     string
		want    string
	}{
		{"no proxies: XFF ignored", nil, "203.0.113.7:5000", "6.6.6.6", "203.0.113.7"},
		{"via trusted proxy: XFF used", []string{"172.28.0.10/32"}, "172.28.0.10:40000", "198.51.100.9", "198.51.100.9"},
		{"via proxy: spoofed entries left of the proxy-appended one are ignored",
			[]string{"172.28.0.10/32"}, "172.28.0.10:40000", "6.6.6.6, 198.51.100.9", "198.51.100.9"},
		{"direct hit bypassing proxy: XFF ignored", []string{"172.28.0.10/32"}, "203.0.113.7:5000", "6.6.6.6", "203.0.113.7"},
		{"v4-mapped peer still matches", []string{"172.28.0.10/32"}, "[::ffff:172.28.0.10]:40000", "198.51.100.9", "198.51.100.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			h := clientIP(tt.trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = middleware.GetClientIP(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.peer
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			assert.Equal(t, tt.want, got)
		})
	}
}
