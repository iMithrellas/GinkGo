//go:build mem

package db

import (
	"context"
	"io"
)

// openSQLite fallback: use in-memory store when sqlite build tag is not enabled.
func openSQLite(ctx context.Context, dsn string) (*Store, io.Closer, error) {
	m := newMemStore()
	return &Store{Events: m, Entries: m}, io.NopCloser(nop{}), nil
}

type nop struct{}

func (nop) Read(p []byte) (int, error) { return 0, io.EOF }
func (nop) Close() error               { return nil }
