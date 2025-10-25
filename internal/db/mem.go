package db

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mithrel/ginkgo/pkg/api"
)

type memStore struct {
	mu     sync.RWMutex
	events []api.Event
	byID   map[string]api.Entry
}

func newMemStore() *memStore {
	return &memStore{byID: make(map[string]api.Entry)}
}

func (m *memStore) Append(ctx context.Context, ev api.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
	if ev.Type == api.EventUpsert && ev.Entry != nil {
		m.byID[ev.Entry.ID] = *ev.Entry
	}
	return nil
}

func (m *memStore) List(ctx context.Context, cur api.Cursor, limit int) ([]api.Event, api.Cursor, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Wireframe: ignore cursor and limit.
	return append([]api.Event(nil), m.events...), api.Cursor{}, nil
}

func (m *memStore) GetEntry(ctx context.Context, id string) (api.Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.byID[id]
	if !ok {
		return api.Entry{}, ErrNotFound
	}
	return e, nil
}

func (m *memStore) CreateEntry(ctx context.Context, e api.Entry) (api.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.ID == "" {
		e.ID = api.NewID()
	}
	if _, exists := m.byID[e.ID]; exists {
		return api.Entry{}, ErrConflict
	}
	if e.Version == 0 {
		e.Version = 1
	}
	m.byID[e.ID] = e
	m.events = append(m.events, api.Event{Time: e.CreatedAt, Type: api.EventUpsert, Entry: &e, ID: e.ID})
	return e, nil
}

func (m *memStore) UpdateEntryCAS(ctx context.Context, e api.Entry, ifVersion int64) (api.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.byID[e.ID]
	if !ok {
		return api.Entry{}, ErrNotFound
	}
	if cur.Version != ifVersion {
		return api.Entry{}, ErrConflict
	}
	e.Version = cur.Version + 1
	if e.CreatedAt.IsZero() {
		e.CreatedAt = cur.CreatedAt
	}
	m.byID[e.ID] = e
	m.events = append(m.events, api.Event{Time: e.UpdatedAt, Type: api.EventUpsert, Entry: &e, ID: e.ID})
	return e, nil
}

func (m *memStore) DeleteEntry(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[id]; !ok {
		return ErrNotFound
	}
	delete(m.byID, id)
	m.events = append(m.events, api.Event{Time: time.Now().UTC(), Type: api.EventDelete, Entry: nil, ID: id})
	return nil
}

func (m *memStore) ListEntries(ctx context.Context, q api.ListQuery) ([]api.Entry, api.Page, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]api.Entry, 0, len(m.byID))
	for _, e := range m.byID {
		if q.Namespace != "" && e.Namespace != q.Namespace {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, api.Page{}, nil
}

func (m *memStore) Search(ctx context.Context, q api.SearchQuery) ([]api.Entry, api.Page, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]api.Entry, 0)
	query := strings.ToLower(q.Query)
	for _, e := range m.byID {
		if q.Namespace != "" && e.Namespace != q.Namespace {
			continue
		}
		hay := strings.ToLower(e.Title + "\n" + e.Body + "\n" + strings.Join(e.Tags, ","))
		if query == "" || strings.Contains(hay, query) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, api.Page{}, nil
}
