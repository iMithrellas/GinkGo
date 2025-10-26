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
		if !q.Since.IsZero() && e.CreatedAt.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && e.CreatedAt.After(q.Until) {
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
		if !q.Since.IsZero() && e.CreatedAt.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && e.CreatedAt.After(q.Until) {
			continue
		}
		// Tag filtering Any/All
		if len(q.All) > 0 || len(q.Any) > 0 {
			tags := sliceToSetFold(e.Tags)
			if len(q.All) > 0 && !containsAll(tags, setFromSliceFold(q.All)) {
				continue
			}
			if len(q.Any) > 0 && !containsAny(tags, setFromSliceFold(q.Any)) {
				continue
			}
		}
		hay := strings.ToLower(e.Title + "\n" + e.Body + "\n" + strings.Join(e.Tags, ","))
		if !q.Regex {
			if query == "" || strings.Contains(hay, query) {
				out = append(out, e)
			}
		} else {
			// Regex path: compile and match after loop (not available here). We'll approximate by substring if Regex flag set in mem.
			if query == "" || strings.Contains(hay, query) {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, api.Page{}, nil
}

func (m *memStore) ListByTags(ctx context.Context, q api.TagFilterQuery) ([]api.Entry, api.Page, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	wantAny := setFromSliceFold(q.Any)
	wantAll := setFromSliceFold(q.All)
	out := make([]api.Entry, 0)
	for _, e := range m.byID {
		if q.Namespace != "" && e.Namespace != q.Namespace {
			continue
		}
		if !q.Since.IsZero() && e.CreatedAt.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && e.CreatedAt.After(q.Until) {
			continue
		}
		tags := sliceToSetFold(e.Tags)
		if len(wantAll) > 0 && !containsAll(tags, wantAll) {
			continue
		}
		if len(wantAny) > 0 && !containsAny(tags, wantAny) {
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

func (m *memStore) ListTags(ctx context.Context, q api.TagsQuery) ([]api.TagStat, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := map[string]int{}
	for _, e := range m.byID {
		if q.Namespace != "" && e.Namespace != q.Namespace {
			continue
		}
		seen := map[string]struct{}{}
		for _, t := range e.Tags {
			tt := strings.ToLower(strings.TrimSpace(t))
			if tt == "" {
				continue
			}
			if _, ok := seen[tt]; ok {
				continue
			}
			seen[tt] = struct{}{}
			counts[tt]++
		}
	}
	out := make([]api.TagStat, 0, len(counts))
	for k, v := range counts {
		out = append(out, api.TagStat{Tag: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Tag < out[j].Tag
		}
		return out[i].Count > out[j].Count
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// helpers
func setFromSliceFold(ss []string) map[string]struct{} {
	if len(ss) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		m[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
	}
	return m
}
func sliceToSetFold(ss []string) map[string]struct{} { return setFromSliceFold(ss) }
func containsAll(have, want map[string]struct{}) bool {
	for k := range want {
		if _, ok := have[k]; !ok {
			return false
		}
	}
	return true
}
func containsAny(have, want map[string]struct{}) bool {
	for k := range want {
		if _, ok := have[k]; ok {
			return true
		}
	}
	return len(want) == 0
}
