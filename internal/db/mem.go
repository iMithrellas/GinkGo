package db

import (
    "context"
    "sync"

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

func (m *memStore) PutEntry(ctx context.Context, e api.Entry) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.byID[e.ID] = e
    return nil
}

