package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/pkg/api"
)

func provenanceEntry() api.Entry {
	now := time.Date(2026, 10, 16, 12, 0, 0, 123, time.UTC)
	return api.Entry{
		ID: "provenance-note", Version: 1, Title: "Clean title", Body: "Clean content",
		Tags: []string{"clean"}, Namespace: "personal", CreatedAt: now, UpdatedAt: now,
		Provenance: &api.Provenance{Sources: []api.Source{{
			ID: "source-1", CapturedAt: now, Kind: "voice", OriginalText: "secretoriginalterm exact\ntranscript",
			Language: "en", STTEngine: "whisper.cpp", STTModel: "base",
		}}},
	}
}

func TestProvenanceMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	dbh, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = dbh.ExecContext(ctx, `CREATE TABLE entries (
id TEXT PRIMARY KEY, version INTEGER NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL,
tags TEXT NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL, namespace TEXT NOT NULL);
INSERT INTO entries VALUES ('old', 1, 'title', 'body', '[]', '2026-10-16 00:00:00', '2026-10-16 00:00:00', 'personal');`)
	require.NoError(t, err)
	require.NoError(t, migrate(ctx, dbh))
	require.NoError(t, migrate(ctx, dbh), "migration must be idempotent")
	var provenance sql.NullString
	var body string
	require.NoError(t, dbh.QueryRowContext(ctx, `SELECT body, provenance FROM entries WHERE id='old'`).Scan(&body, &provenance))
	require.False(t, provenance.Valid)
	require.Equal(t, "body", body)
	var count int
	require.NoError(t, dbh.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries_fts`).Scan(&count))
	require.Zero(t, count, "adding the column must not rebuild FTS")
	require.NoError(t, dbh.Close())
}

func TestProvenancePersistenceCASAndEvents(t *testing.T) {
	store, ctx, cancel := setupTestDB(t)
	defer cancel()
	initial := provenanceEntry()
	created, err := store.Entries.CreateEntry(ctx, initial)
	require.NoError(t, err)
	require.Equal(t, initial.Provenance, created.Provenance)
	got, err := store.Entries.GetEntry(ctx, initial.ID)
	require.NoError(t, err)
	require.Equal(t, initial.Provenance, got.Provenance)

	// A legacy-style edit has no metadata but must retain the stored originals.
	edit := got
	edit.Provenance = nil
	edit.Body = "Edited clean content"
	edit.Version++
	updated, err := store.Entries.UpdateEntryCAS(ctx, edit, got.Version)
	require.NoError(t, err)
	require.Equal(t, initial.Provenance, updated.Provenance)

	expansion := updated
	expansion.Provenance = &api.Provenance{Sources: append([]api.Source(nil), updated.Provenance.Sources...)}
	expansion.Provenance.Sources = append(expansion.Provenance.Sources, api.Source{
		ID: "source-2", CapturedAt: initial.CreatedAt.Add(time.Minute), Kind: "text", OriginalText: "second input",
	})
	expansion.Version++
	updated, err = store.Entries.UpdateEntryCAS(ctx, expansion, updated.Version)
	require.NoError(t, err)
	require.Len(t, updated.Provenance.Sources, 2)

	events, _, err := store.Events.List(ctx, api.Cursor{}, 100)
	require.NoError(t, err)
	require.Len(t, events, 3)
	for i, event := range events {
		var snapshot api.Entry
		require.NoError(t, json.Unmarshal(event.Payload, &snapshot))
		require.Equal(t, int64(i+1), snapshot.Version)
		require.Equal(t, initial.Provenance.Sources[0], snapshot.Provenance.Sources[0])
		if i < 2 {
			require.Len(t, snapshot.Provenance.Sources, 1)
		} else {
			require.Equal(t, updated.Provenance, snapshot.Provenance)
		}
	}

	for _, mutate := range []func(*api.Entry){
		func(e *api.Entry) { e.Provenance = &api.Provenance{} },
		func(e *api.Entry) { e.Provenance.Sources[0].OriginalText = "changed" },
		func(e *api.Entry) {
			e.Provenance.Sources[0], e.Provenance.Sources[1] = e.Provenance.Sources[1], e.Provenance.Sources[0]
		},
	} {
		bad, err := store.Entries.GetEntry(ctx, initial.ID)
		require.NoError(t, err)
		mutate(&bad)
		bad.Version++
		_, err = store.Entries.UpdateEntryCAS(ctx, bad, updated.Version)
		require.ErrorIs(t, err, ErrConflict)
	}
	_, err = store.Entries.UpdateEntryCAS(ctx, edit, 1)
	require.ErrorIs(t, err, ErrConflict)
	final, err := store.Entries.GetEntry(ctx, initial.ID)
	require.NoError(t, err)
	require.Equal(t, updated, final)
	events, _, err = store.Events.List(ctx, api.Cursor{}, 100)
	require.NoError(t, err)
	require.Len(t, events, 3, "failed CAS must not append events")
}

func TestProvenanceSearchIsolationAndListing(t *testing.T) {
	store, ctx, cancel := setupTestDB(t)
	defer cancel()
	initial := provenanceEntry()
	_, err := store.Entries.CreateEntry(ctx, initial)
	require.NoError(t, err)
	for _, regex := range []bool{false, true} {
		entries, _, err := store.Entries.Search(ctx, api.SearchQuery{Query: "secretoriginalterm", Regex: regex})
		require.NoError(t, err)
		require.Empty(t, entries)
		entries, _, err = store.Entries.Search(ctx, api.SearchQuery{Query: "Clean", Regex: regex})
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, initial.Provenance, entries[0].Provenance)
	}
	for _, body := range []bool{false, true} {
		entries, _, err := store.Entries.ListEntries(ctx, api.ListQuery{IncludeBody: body})
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, initial.Provenance, entries[0].Provenance)
		if !body {
			require.Empty(t, entries[0].Body)
		}
	}
}

func TestProvenanceReplicationPreservation(t *testing.T) {
	store, ctx, cancel := setupTestDB(t)
	defer cancel()
	e := provenanceEntry()
	require.NoError(t, store.ApplyReplicationBatch(ctx, []api.Event{{Type: api.EventUpsert, Entry: &e}}))
	legacy := e
	legacy.Provenance = nil
	legacy.Version++
	legacy.Body = "Remote edit"
	require.NoError(t, store.ApplyReplicationBatch(ctx, []api.Event{{Type: api.EventUpsert, Entry: &legacy}}))
	got, err := store.Entries.GetEntry(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, e.Provenance, got.Provenance)
	require.Equal(t, legacy.Body, got.Body)
	events, _, err := store.Events.List(ctx, api.Cursor{}, 100)
	require.NoError(t, err)
	require.Empty(t, events, "replication must not echo events")

	bad := legacy
	bad.Provenance = &api.Provenance{}
	bad.Body = "must not persist"
	require.ErrorIs(t, store.ApplyReplication(ctx, api.Event{Type: api.EventUpsert, Entry: &bad}), ErrConflict)
	final, err := store.Entries.GetEntry(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, got, final)
}

func TestProvenanceEventFailureRollsBackEntry(t *testing.T) {
	store, ctx, cancel := setupTestDB(t)
	defer cancel()
	e := provenanceEntry()
	_, err := store.Entries.CreateEntry(ctx, e)
	require.NoError(t, err)
	sqlite := store.Entries.(*sqliteStore)
	_, err = sqlite.db.ExecContext(ctx, `CREATE TRIGGER reject_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'event failure'); END`)
	require.NoError(t, err)
	e.Provenance.Sources = append(e.Provenance.Sources, api.Source{ID: "new", Kind: "text", OriginalText: "new original"})
	e.Version++
	e.Body = "failed update"
	_, err = store.Entries.UpdateEntryCAS(ctx, e, 1)
	require.Error(t, err)
	got, err := store.Entries.GetEntry(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, provenanceEntry(), got)
}

func TestProvenanceEquivalentTimezonePrefix(t *testing.T) {
	store, ctx, cancel := setupTestDB(t)
	defer cancel()
	e := provenanceEntry()
	_, err := store.Entries.CreateEntry(ctx, e)
	require.NoError(t, err)
	e.Provenance.Sources[0].CapturedAt = e.Provenance.Sources[0].CapturedAt.In(time.FixedZone("offset", -7*3600))
	e.Version++
	updated, err := store.Entries.UpdateEntryCAS(ctx, e, 1)
	require.NoError(t, err)
	require.Equal(t, e.Hash(), updated.Hash())
}

func TestProvenanceAllSourceFieldsImmutable(t *testing.T) {
	mutations := map[string]func(*api.Source){
		"id":            func(s *api.Source) { s.ID = "different" },
		"captured_at":   func(s *api.Source) { s.CapturedAt = s.CapturedAt.Add(time.Nanosecond) },
		"kind":          func(s *api.Source) { s.Kind = "text" },
		"original_text": func(s *api.Source) { s.OriginalText = "different" },
		"language":      func(s *api.Source) { s.Language = "fr" },
		"stt_engine":    func(s *api.Source) { s.STTEngine = "different" },
		"stt_model":     func(s *api.Source) { s.STTModel = "small" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store, ctx, cancel := setupTestDB(t)
			defer cancel()
			e := provenanceEntry()
			_, err := store.Entries.CreateEntry(ctx, e)
			require.NoError(t, err)
			mutate(&e.Provenance.Sources[0])
			e.Version++
			_, err = store.Entries.UpdateEntryCAS(ctx, e, 1)
			require.ErrorIs(t, err, ErrConflict)
			got, err := store.Entries.GetEntry(ctx, e.ID)
			require.NoError(t, err)
			require.Equal(t, provenanceEntry(), got)
		})
	}
}

func TestProvenanceReplicationStaleHistory(t *testing.T) {
	for _, history := range []string{"prefix", "nil", "same", "timezone"} {
		t.Run(history, func(t *testing.T) {
			store, ctx, cancel := setupTestDB(t)
			defer cancel()
			old := provenanceEntry()
			current := provenanceEntry()
			current.Version = 3
			current.Body = "latest cleaned body"
			if history != "same" {
				current.Provenance.Sources = append(current.Provenance.Sources, api.Source{ID: "second", Kind: "text", OriginalText: "next"})
			}
			if history == "nil" {
				old.Provenance = nil
			}
			if history == "timezone" {
				old.Provenance.Sources[0].CapturedAt = old.Provenance.Sources[0].CapturedAt.In(time.FixedZone("offset", 3600))
			}
			_, err := store.Entries.CreateEntry(ctx, current)
			require.NoError(t, err)
			unrelated := api.Entry{ID: "other", Body: "other body", CreatedAt: old.CreatedAt, UpdatedAt: old.UpdatedAt}
			require.NoError(t, store.ApplyReplicationBatch(ctx, []api.Event{
				{Type: api.EventUpsert, Entry: &unrelated},
				{Type: api.EventUpsert, Entry: &old},
			}))
			got, err := store.Entries.GetEntry(ctx, current.ID)
			require.NoError(t, err)
			require.Equal(t, current, got, "stale snapshot must not downgrade content, version, or sources")
			_, err = store.Entries.GetEntry(ctx, unrelated.ID)
			require.NoError(t, err, "other events in the batch must commit")
		})
	}
}

func TestProvenanceReplicationDivergenceRollsBackBatch(t *testing.T) {
	for _, version := range []int64{1, 2, 3} {
		store, ctx, cancel := setupTestDB(t)
		e := provenanceEntry()
		e.Version = 2
		_, err := store.Entries.CreateEntry(ctx, e)
		require.NoError(t, err)
		bad := provenanceEntry()
		bad.Version = version
		bad.Provenance.Sources[0].OriginalText = "divergent original"
		other := provenanceEntry()
		other.ID = "must-rollback"
		err = store.ApplyReplicationBatch(ctx, []api.Event{
			{Type: api.EventUpsert, Entry: &other},
			{Type: api.EventUpsert, Entry: &bad},
		})
		require.ErrorIs(t, err, ErrConflict)
		_, err = store.Entries.GetEntry(ctx, other.ID)
		require.ErrorIs(t, err, ErrNotFound)
		got, err := store.Entries.GetEntry(ctx, e.ID)
		require.NoError(t, err)
		require.Equal(t, e, got)
		events, _, err := store.Events.List(ctx, api.Cursor{}, 100)
		require.NoError(t, err)
		require.Len(t, events, 1)
		cancel()
	}
}

func TestReplicationIdenticalContentAdvancesVersion(t *testing.T) {
	store, ctx, cancel := setupTestDB(t)
	defer cancel()
	e := provenanceEntry()
	_, err := store.Entries.CreateEntry(ctx, e)
	require.NoError(t, err)
	e.Version++ // Hash deliberately excludes version.
	require.NoError(t, store.ApplyReplication(ctx, api.Event{Type: api.EventUpsert, Entry: &e}))
	got, err := store.Entries.GetEntry(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, e, got)
}
