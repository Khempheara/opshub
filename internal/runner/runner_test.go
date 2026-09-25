package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tarGz(t *testing.T, entries ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = "content of " + h.Name
			h.Size = int64(len(body))
		}
		require.NoError(t, tw.WriteHeader(h))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func names(t *testing.T, r io.Reader) []string {
	t.Helper()
	var out []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		n := h.Name
		if h.Typeflag == tar.TypeLink {
			n += "->" + h.Linkname
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func TestStripSource(t *testing.T) {
	src := tarGz(t,
		&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "sha"}},
		&tar.Header{Typeflag: tar.TypeDir, Name: "acme-api-abc/", Mode: 0o755},
		&tar.Header{Typeflag: tar.TypeReg, Name: "acme-api-abc/README.md", Mode: 0o644},
		&tar.Header{Typeflag: tar.TypeDir, Name: "acme-api-abc/src/", Mode: 0o755},
		&tar.Header{Typeflag: tar.TypeReg, Name: "acme-api-abc/src/main.go", Mode: 0o644},
		&tar.Header{Typeflag: tar.TypeLink, Name: "acme-api-abc/src/copy.go", Linkname: "acme-api-abc/src/main.go"},
		&tar.Header{Typeflag: tar.TypeReg, Name: "acme-api-abc/../../etc/passwd", Mode: 0o644},
	)
	r := StripSource(bytes.NewReader(src))
	defer func() { _ = r.Close() }()
	assert.Equal(t, []string{"README.md", "src/", "src/copy.go->src/main.go", "src/main.go"}, names(t, r))

	bad := StripSource(strings.NewReader("not gzip"))
	_, err := io.ReadAll(bad)
	assert.ErrorContains(t, err, "source archive")
}

func TestRepack(t *testing.T) {
	// Docker names entries after the requested path's last element ("dist" for build/dist).
	var in bytes.Buffer
	tw := tar.NewWriter(&in)
	for _, h := range []*tar.Header{
		{Typeflag: tar.TypeDir, Name: "dist/", Mode: 0o755},
		{Typeflag: tar.TypeReg, Name: "dist/app", Mode: 0o755},
		{Typeflag: tar.TypeReg, Name: "../escape", Mode: 0o644},
	} {
		body := "x"
		if h.Typeflag == tar.TypeReg {
			h.Size = 1
		} else {
			body = ""
		}
		require.NoError(t, tw.WriteHeader(h))
		_, _ = tw.Write([]byte(body))
	}
	require.NoError(t, tw.Close())

	var out bytes.Buffer
	ow := tar.NewWriter(&out)
	n, err := Repack(ow, &in, "build")
	require.NoError(t, err)
	require.NoError(t, ow.Close())
	assert.Equal(t, 3, n) // "../escape" re-rooted at build/ stays inside: "escape"
	assert.Equal(t, []string{"build/dist/", "build/dist/app", "escape"}, names(t, &out))
}

func TestSafeRel(t *testing.T) {
	for in, want := range map[string]string{"dist/": "dist", "./a/../b": "b", "a/b": "a/b"} {
		got, err := safeRel(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got)
	}
	for _, bad := range []string{"", "/etc", "..", "../x", "a/../../x"} {
		_, err := safeRel(bad)
		assert.Error(t, err, bad)
	}
}

type sent struct {
	mu     sync.Mutex
	chunks []string
	seqs   []int
}

func (s *sent) send(_ context.Context, seq int, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seqs = append(s.seqs, seq)
	s.chunks = append(s.chunks, content)
	return nil
}

func TestLogShipper(t *testing.T) {
	var s sent
	l := NewLogShipper(s.send, []string{"hunter22", "abc"}, nil)
	_, _ = l.Write([]byte("password is hunter22\npartial"))
	_, _ = l.Write([]byte(" line abc\x00\n"))
	big := strings.Repeat("y", logChunkBytes+10) // no newline: cut at the chunk size
	_, _ = l.Write([]byte(big))
	l.Close()

	all := strings.Join(s.chunks, "")
	assert.Equal(t, "password is ••••••\npartial line abc\n"+big, all, "short secrets aren't masked; NUL is dropped")
	for i, seq := range s.seqs {
		assert.Equal(t, i+1, seq)
	}
	for _, c := range s.chunks {
		assert.LessOrEqual(t, len(c), logChunkBytes)
	}
}

func TestLogShipperJobGone(t *testing.T) {
	gone := make(chan struct{})
	l := NewLogShipper(func(context.Context, int, string) error {
		return &APIError{Status: 409, Code: "JOB_NOT_RUNNING"}
	}, nil, func() { close(gone) })
	_, _ = l.Write([]byte("hello\n"))
	l.Close()
	select {
	case <-gone:
	default:
		t.Fatal("onGone not called")
	}
}

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.json")
	c := Config{URL: "https://ops.example.com", Token: "ohr_abc_secret", Name: "r1"}
	require.NoError(t, SaveConfig(path, c))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	got, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 1, got.MaxConcurrency)
	assert.Equal(t, "/var/run/docker.sock", got.DockerSocket)
	assert.Equal(t, "alpine:3.20", got.DefaultImage)

	require.NoError(t, os.Chmod(path, 0o644))
	_, err = LoadConfig(path)
	assert.ErrorContains(t, err, "chmod 600")

	for _, bad := range []Config{
		{URL: "ftp://x", Token: "ohr_a_b"},
		{URL: "https://x", Token: "ohr_reg_x"},
		{URL: "https://x", Token: "ohr_a_b", MaxConcurrency: 65},
		{URL: "https://x", Token: "ohr_a_b", CPUs: -1},
	} {
		assert.Error(t, bad.Validate())
	}
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "512 B", humanBytes(512))
	assert.Equal(t, "1.5 KiB", humanBytes(1536))
	assert.Equal(t, "2.0 MiB", humanBytes(2<<20))
}
