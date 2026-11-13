package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Local keeps files in a directory on disk. It is meant for one server or a shared volume.
// Every access goes through os.Root, so a key can never reach outside the directory, not even through
// symbolic links.
type Local struct {
	root *os.Root
}

// NewLocal opens (and creates, when missing) the directory dir.
func NewLocal(dir string) (*Local, error) {
	if dir == "" {
		return nil, errors.New("storage: directory is empty")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("storage: create %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", dir, err)
	}
	return &Local{root: root}, nil
}

// Close releases the directory handle.
func (l *Local) Close() error { return l.root.Close() }

// cleanKey accepts "a/b/c" style keys of plain characters and nothing else.
func cleanKey(key string) (string, error) {
	if key == "" || len(key) > 300 || strings.HasPrefix(key, "/") || path.Clean(key) != key {
		return "", ErrInvalidKey
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", ErrInvalidKey
		}
	}
	for _, r := range key {
		ok := r == '/' || r == '-' || r == '_' || r == '.' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return "", ErrInvalidKey
		}
	}
	return key, nil
}

// Put writes to a temporary file in the same directory and renames it, so a reader never sees half a file and
// a failed upload leaves nothing behind.
func (l *Local) Put(ctx context.Context, key string, r io.Reader, max int64) (n int64, err error) {
	key, err = cleanKey(key)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	dir := path.Dir(key)
	if dir != "." {
		if err := l.root.MkdirAll(dir, 0o750); err != nil {
			return 0, fmt.Errorf("storage: mkdir: %w", err)
		}
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return 0, fmt.Errorf("storage: random: %w", err)
	}
	tmp := path.Join(dir, ".tmp-"+hex.EncodeToString(rnd[:]))
	f, err := l.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, fmt.Errorf("storage: create: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = l.root.Remove(tmp)
		}
	}()

	n, err = io.Copy(f, io.LimitReader(&ctxReader{ctx, r}, max+1))
	if err != nil {
		return 0, fmt.Errorf("storage: write: %w", err)
	}
	if n > max {
		return 0, ErrTooLarge
	}
	if err = f.Close(); err != nil {
		return 0, fmt.Errorf("storage: close: %w", err)
	}
	if err = l.root.Rename(tmp, key); err != nil {
		return 0, fmt.Errorf("storage: rename: %w", err)
	}
	return n, nil
}

// Open implements Store.
func (l *Local) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	key, err := cleanKey(key)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := l.root.Open(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	return f, nil
}

// Delete implements Store.
func (l *Local) Delete(_ context.Context, key string) error {
	key, err := cleanKey(key)
	if err != nil {
		return err
	}
	if err := l.root.Remove(key); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: delete: %w", err)
	}
	return nil
}

// ctxReader stops a long copy when the request is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
