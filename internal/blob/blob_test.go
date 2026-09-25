package blob

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalStore(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(dir)
	require.NoError(t, err)
	ctx := context.Background()

	n, sum, err := s.Put(ctx, "artifacts/abc/archive.tar.gz", strings.NewReader("hello"), 10)
	require.NoError(t, err)
	assert.EqualValues(t, 5, n)
	want := sha256.Sum256([]byte("hello"))
	assert.Equal(t, want[:], sum)

	r, size, err := s.Get(ctx, "artifacts/abc/archive.tar.gz")
	require.NoError(t, err)
	b, _ := io.ReadAll(r)
	_ = r.Close()
	assert.Equal(t, "hello", string(b))
	assert.EqualValues(t, 5, size)

	// Too large: nothing is stored, and no temp files are left behind.
	_, _, err = s.Put(ctx, "artifacts/big", strings.NewReader("0123456789x"), 10)
	assert.ErrorIs(t, err, ErrTooLarge)
	_, _, err = s.Get(ctx, "artifacts/big")
	assert.ErrorIs(t, err, ErrNotFound)
	entries, _ := os.ReadDir(filepath.Join(dir, "artifacts"))
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".upload-"), e.Name())
	}

	require.NoError(t, s.Delete(ctx, "artifacts/abc/archive.tar.gz"))
	require.NoError(t, s.Delete(ctx, "artifacts/abc/archive.tar.gz"), "deleting twice is fine")
	_, _, err = s.Get(ctx, "artifacts/abc/archive.tar.gz")
	assert.ErrorIs(t, err, ErrNotFound)

	for _, bad := range []string{"", "../etc/passwd", "a/../b", "/abs", "a//b", "UPPER/x", "a/.hidden"} {
		_, _, err := s.Put(ctx, bad, strings.NewReader("x"), 10)
		assert.ErrorIs(t, err, ErrBadKey, bad)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = s.Put(canceled, "cache/x", strings.NewReader("data"), 10)
	assert.ErrorIs(t, err, context.Canceled)
}
