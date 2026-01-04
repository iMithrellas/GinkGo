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
	DeleteNamespace(ctx context.Context, namespace string) (int64, error)
	ListEntries(ctx context.Context, q api.ListQuery) ([]api.Entry, api.Page, error)
	Search(ctx context.Context, q api.SearchQuery) ([]api.Entry, api.Page, error)
	ListTags(ctx context.Context, q api.TagsQuery) ([]api.TagStat, error)
	ListNamespaces(ctx context.Context) ([]string, error)
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

// noEventLogKey marks contexts where event-log appends should be skipped.
type noEventLogKey struct{}

// WithNoEventLog returns a context that instructs the DB layer to skip
// appending to the local events log (used for replication apply).
func WithNoEventLog(ctx context.Context) context.Context {
	return context.WithValue(ctx, noEventLogKey{}, true)
}

// shouldLog reports whether event-log appends should occur for this context.
func shouldLog(ctx context.Context) bool {
	v, _ := ctx.Value(noEventLogKey{}).(bool)
	return !v
}

// ApplyReplication applies an incoming event from a remote without appending
// a new local event log entry. This prevents echoing pulled changes back out.
func (s *Store) ApplyReplication(ctx context.Context, ev api.Event) error {
	ctx = WithNoEventLog(ctx)
	switch ev.Type {
	case api.EventUpsert:
		if ev.Entry == nil {
			return nil
		}
		// Idempotency check: if we already have this EXACT entry, skip write.
		cur, err := s.Entries.GetEntry(ctx, ev.Entry.ID)
		if err == nil {
			// Found local entry. Check hash.
			if cur.Hash() == ev.Entry.Hash() {
				return nil
			}
			// If different, overwrite logic below will handle it.
		} else if err != ErrNotFound {
			return err
		}

		if _, err := s.Entries.CreateEntry(ctx, *ev.Entry); err != nil {
			if err == ErrConflict {
				// We already checked GetEntry above, so we know it exists.
				// But concurrent writes could happen, so this is safe fallback.
				cur, err := s.Entries.GetEntry(ctx, ev.Entry.ID)
				if err != nil {
					return err
				}
				_, err = s.Entries.UpdateEntryCAS(ctx, *ev.Entry, cur.Version)
				return err
			}
			return err
		}
		return nil
	case api.EventDelete:
		if err := s.Entries.DeleteEntry(ctx, ev.ID); err != nil && err != ErrNotFound {
			return err
		}
		return nil
	default:
		return nil
	}
}

// ApplyReplicationBatch applies a batch of events using a single transaction when supported.
func (s *Store) ApplyReplicationBatch(ctx context.Context, evs []api.Event) error {
	if len(evs) == 0 {
		return nil
	}
	ctx = WithNoEventLog(ctx)
	if tp, ok := s.Entries.(TxProvider); ok {
		tx, err := tp.BeginTx(ctx)
		if err != nil {
			return err
		}
		ctx = WithTx(ctx, tx)
		for _, ev := range evs {
			if err := s.ApplyReplication(ctx, ev); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		return tx.Commit()
	}
	for _, ev := range evs {
		if err := s.ApplyReplication(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}
