// Package storage keeps the bytes of uploaded files. The database holds the metadata (who, what, how big);
// a Store holds the content under a key chosen by the server.
//
// Store is small on purpose, so another backend (S3, GCS, MinIO) is one more type that satisfies it:
// nothing outside this package and the line that picks the driver in cmd/server changes. See docs/files.md.
package storage

import (
	"context"
	"errors"
	"io"
)

var (
	// ErrNotFound: nothing is stored under the key.
	ErrNotFound = errors.New("storage: not found")
	// ErrTooLarge: the content is larger than the limit given to Put. Nothing was stored.
	ErrTooLarge = errors.New("storage: content too large")
	// ErrInvalidKey: the key is empty, absolute, or tries to leave the storage (..).
	ErrInvalidKey = errors.New("storage: invalid key")
)

// Store saves and serves file content. Keys look like "<tenant_id>/<file_id>" and are made by the server,
// never taken from a client. Implementations must be safe for concurrent use.
type Store interface {
	// Put stores the content of r under key, replacing what was there. It reads at most max bytes: with more it
	// stores nothing and returns ErrTooLarge. It returns the number of bytes stored.
	Put(ctx context.Context, key string, r io.Reader, max int64) (int64, error)
	// Open returns the content. The caller closes it. A missing key is ErrNotFound.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the content. A missing key is not an error.
	Delete(ctx context.Context, key string) error
}
