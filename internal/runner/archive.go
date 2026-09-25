package runner

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// safeRel cleans a workspace-relative path and rejects absolute paths and escapes.
func safeRel(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must be relative to the workspace", p)
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("path %q leaves the workspace", p)
	}
	return c, nil
}

// StripSource converts a gzip-compressed Git host tarball (all entries under one top-level
// directory) into an uncompressed tar stream rooted at the workspace. Entries that would
// land outside the workspace are dropped.
func StripSource(gz io.Reader) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(stripSource(gz, pw))
	}()
	return pr
}

func stripSource(gz io.Reader, w io.Writer) error {
	zr, err := gzip.NewReader(gz)
	if err != nil {
		return fmt.Errorf("source archive: %w", err)
	}
	tr := tar.NewReader(zr)
	tw := tar.NewWriter(w)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("source archive: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, ok := stripFirst(h.Name)
		if !ok {
			continue
		}
		if h.Typeflag == tar.TypeLink {
			if h.Linkname, ok = stripFirst(h.Linkname); !ok {
				continue
			}
		}
		h.Name = name
		h.Format = tar.FormatPAX
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil { // #nosec G110 -- size is bounded by the server's source limit
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, zr) // #nosec G110 -- drains the tail so the gzip checksum is verified; size bounded by the server's source limit
	return err
}

// stripFirst drops the first path element ("repo-sha/src/a.go" → "src/a.go").
func stripFirst(name string) (string, bool) {
	_, rest, ok := strings.Cut(strings.TrimPrefix(name, "./"), "/")
	if !ok || rest == "" {
		return "", false
	}
	rel, err := safeRel(rest)
	if err != nil {
		return "", false
	}
	if strings.HasSuffix(rest, "/") {
		rel += "/"
	}
	return rel, true
}

// Repack copies the entries of a Docker archive (named after the requested path's last
// element) into tw, re-rooted at dir so they keep their workspace-relative path. It
// returns the number of entries copied.
func Repack(tw *tar.Writer, r io.Reader, dir string) (int, error) {
	tr := tar.NewReader(r)
	n := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		name, err := safeRel(path.Join(dir, h.Name)) // #nosec G305 -- safeRel rejects absolute paths and escapes
		if err != nil {
			continue
		}
		if h.Typeflag == tar.TypeDir {
			name += "/"
		}
		if h.Typeflag == tar.TypeLink {
			link, err := safeRel(path.Join(dir, h.Linkname)) // #nosec G305 -- safeRel rejects absolute paths and escapes
			if err != nil {
				continue
			}
			h.Linkname = link
		}
		h.Name = name
		h.Format = tar.FormatPAX
		if err := tw.WriteHeader(h); err != nil {
			return n, err
		}
		if _, err := io.Copy(tw, tr); err != nil { // #nosec G110 -- the job's own workspace
			return n, err
		}
		n++
	}
}
