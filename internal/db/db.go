package db

import (
	"context"
	"errors"
	"io"

	"github.com/mithrel/ginkgo/pkg/api"
)

// Event log (for A/A later)
type EventLog interface {
	Append(ctx context.Context, ev api.Event) error
	List(ctx context.Context, cur api.Cursor, limit int) ([]api.Event, api.Cursor, error)
}

// Materialized entries
type EntryRepo interface {
	GetEntry(ctx context.Context, id string) (api.Entry, error)
	CreateEntry(ctx context.Context, e api.Entry) (api.Entry, error)
	UpdateEntryCAS(ctx context.Context, e api.Entry, ifVersion int64) (api.Entry, error)
	DeleteEntry(ctx context.Context, id string) error
	ListEntries(ctx context.Context, q api.ListQuery) ([]api.Entry, api.Page, error)
	Search(ctx context.Context, q api.SearchQuery) ([]api.Entry, api.Page, error)
}

type Store struct {
	Events  EventLog
	Entries EntryRepo
	io.Closer
}

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

func Open(ctx context.Context, dsn string) (*Store, error) {
	s, closer, err := openSQLite(ctx, dsn)
	if err != nil {
		return nil, err
	}
	s.Closer = closer
	return s, nil
}
