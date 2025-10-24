package db

import (
    "context"
    "errors"

    "github.com/mithrel/ginkgo/pkg/api"
)

// Store is the abstract event log and query interface.
type Store interface {
    Append(ctx context.Context, ev api.Event) error
    List(ctx context.Context, cur api.Cursor, limit int) ([]api.Event, api.Cursor, error)
    GetEntry(ctx context.Context, id string) (api.Entry, error)
    PutEntry(ctx context.Context, e api.Entry) error
}

var ErrNotFound = errors.New("not found")

// Open returns a Store based on a URL (sqlite://, postgres://, etc.).
func Open(ctx context.Context, url string) (Store, error) {
    // Wireframe: return an in-memory placeholder.
    return newMemStore(), nil
}

