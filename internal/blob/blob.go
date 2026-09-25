// Package blob stores large binary objects (pipeline artifacts and caches) outside the
// database. The local implementation keeps them on a filesystem volume (decision D5); a
// shared volume is needed when several API replicas run.
package blob

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// Errors.
var (
	ErrNotFound = errors.New("blob not found")
	ErrTooLarge = errors.New("blob exceeds the size limit")
	ErrBadKey   = errors.New("invalid blob key")
)

// Store is where blobs live.
type Store interface {
	// Put stores r under key, failing with ErrTooLarge (and storing nothing) past maxBytes.
	// It returns the size and SHA-256 of what was stored.
	Put(ctx context.Context, key string, r io.Reader, maxBytes int64) (int64, []byte, error)
	// Get opens a blob for reading.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	// Delete removes a blob; missing blobs are not an error.
	Delete(ctx context.Context, key string) error
}

// Keys are relative slash-separated paths of safe characters (no "..").
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

func validKey(key string) bool { return keyPattern.MatchString(key) && len(key) <= 300 }

// Local stores blobs as files under a directory.
type Local struct {
	dir string
}

// NewLocal creates dir if needed.
func NewLocal(dir string) (*Local, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("blob dir: %w", err)
	}
	return &Local{dir: dir}, nil
}

func (l *Local) path(key string) (string, error) {
	if !validKey(key) {
		return "", ErrBadKey
	}
	return filepath.Join(l.dir, filepath.FromSlash(key)), nil
}

func (l *Local) Put(ctx context.Context, key string, r io.Reader, maxBytes int64) (int64, []byte, error) {
	dst, err := l.path(key)
	if err != nil {
		return 0, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return 0, nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upload-*")
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(ctxReader{ctx, r}, maxBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, nil, err
	}
	if n > maxBytes {
		return 0, nil, ErrTooLarge
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return 0, nil, err
	}
	return n, h.Sum(nil), nil
}

func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p) // #nosec G304 -- key validated by keyPattern; confined to the blob dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (l *Local) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ctxReader stops a copy when the request is canceled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
