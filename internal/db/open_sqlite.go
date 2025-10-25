package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mithrel/ginkgo/pkg/api"
)

type sqliteStore struct{ db *sql.DB }

// EventLog
func (s *sqliteStore) Append(ctx context.Context, ev api.Event) error {
	var entryJSON []byte
	if ev.Entry != nil {
		b, _ := json.Marshal(ev.Entry)
		entryJSON = b
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO events(time, type, id, entry_json) VALUES(?,?,?,?)`, ev.Time.UTC(), string(ev.Type), ev.ID, entryJSON)
	return err
}

func (s *sqliteStore) List(ctx context.Context, cur api.Cursor, limit int) ([]api.Event, api.Cursor, error) {
	// Minimal implementation: list by rowid/time
	rows, err := s.db.QueryContext(ctx, `SELECT time, type, id, entry_json FROM events ORDER BY time ASC`)
	if err != nil {
		return nil, api.Cursor{}, err
	}
	defer rows.Close()
	var out []api.Event
	for rows.Next() {
		var t time.Time
		var typ string
		var id string
		var ej []byte
		if err := rows.Scan(&t, &typ, &id, &ej); err != nil {
			return nil, api.Cursor{}, err
		}
		var e *api.Entry
		if len(ej) > 0 {
			_ = json.Unmarshal(ej, &e)
		}
		out = append(out, api.Event{Time: t, Type: api.EventType(typ), ID: id, Entry: e})
	}
	return out, api.Cursor{}, nil
}

// EntryRepo
func (s *sqliteStore) GetEntry(ctx context.Context, id string) (api.Entry, error) {
	var e api.Entry
	var tagsJSON string
	row := s.db.QueryRowContext(ctx, `SELECT id, version, title, body, tags, created_at, updated_at, namespace FROM entries WHERE id=?`, id)
	if err := row.Scan(&e.ID, &e.Version, &e.Title, &e.Body, &tagsJSON, &e.CreatedAt, &e.UpdatedAt, &e.Namespace); err != nil {
		if err == sql.ErrNoRows {
			return api.Entry{}, ErrNotFound
		}
		return api.Entry{}, err
	}
	_ = json.Unmarshal([]byte(tagsJSON), &e.Tags)
	return e, nil
}

func (s *sqliteStore) CreateEntry(ctx context.Context, e api.Entry) (api.Entry, error) {
	if e.ID == "" {
		return api.Entry{}, ErrConflict
	}
	if e.Version == 0 {
		e.Version = 1
	}
	tagsJSON, _ := json.Marshal(e.Tags)
	_, err := s.db.ExecContext(ctx, `INSERT INTO entries(id, version, title, body, tags, created_at, updated_at, namespace) VALUES(?,?,?,?,?,?,?,?)`,
		e.ID, e.Version, e.Title, e.Body, string(tagsJSON), e.CreatedAt.UTC(), e.UpdatedAt.UTC(), e.Namespace)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return api.Entry{}, ErrConflict
		}
		return api.Entry{}, err
	}
	_ = s.Append(ctx, api.Event{Time: e.CreatedAt, Type: api.EventUpsert, ID: e.ID, Entry: &e})
	// FTS index
	_, _ = s.db.ExecContext(ctx, `INSERT INTO entries_fts(rowid, title, body, tags, namespace, id) VALUES((SELECT rowid FROM entries WHERE id=?), ?, ?, ?, ?, ?)`, e.ID, e.Title, e.Body, string(tagsJSON), e.Namespace, e.ID)
	return e, nil
}

func (s *sqliteStore) UpdateEntryCAS(ctx context.Context, e api.Entry, ifVersion int64) (api.Entry, error) {
	// Try CAS update
	tagsJSON, _ := json.Marshal(e.Tags)
	res, err := s.db.ExecContext(ctx, `UPDATE entries SET version=version+1, title=?, body=?, tags=?, updated_at=?, namespace=? WHERE id=? AND version=?`,
		e.Title, e.Body, string(tagsJSON), e.UpdatedAt.UTC(), e.Namespace, e.ID, ifVersion)
	if err != nil {
		return api.Entry{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.Entry{}, ErrConflict
	}
	// Read back
	ne, err := s.GetEntry(ctx, e.ID)
	if err == nil {
		// Refresh FTS row
		_, _ = s.db.ExecContext(ctx, `DELETE FROM entries_fts WHERE id=?`, ne.ID)
		_, _ = s.db.ExecContext(ctx, `INSERT INTO entries_fts(rowid, title, body, tags, namespace, id) VALUES((SELECT rowid FROM entries WHERE id=?), ?, ?, ?, ?, ?)`, ne.ID, ne.Title, ne.Body, string(tagsJSON), ne.Namespace, ne.ID)
	}
	return ne, err
}

func (s *sqliteStore) DeleteEntry(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM entries WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	_ = s.Append(ctx, api.Event{Time: time.Now().UTC(), Type: api.EventDelete, ID: id})
	_, _ = s.db.ExecContext(ctx, `DELETE FROM entries_fts WHERE id=?`, id)
	return nil
}

func (s *sqliteStore) ListEntries(ctx context.Context, q api.ListQuery) ([]api.Entry, api.Page, error) {
	var rows *sql.Rows
	var err error
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	if q.Namespace != "" {
		rows, err = s.db.QueryContext(ctx, `SELECT id, version, title, body, tags, created_at, updated_at, namespace FROM entries WHERE namespace=? ORDER BY created_at DESC LIMIT ?`, q.Namespace, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id, version, title, body, tags, created_at, updated_at, namespace FROM entries ORDER BY created_at DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, api.Page{}, err
	}
	defer rows.Close()
	var out []api.Entry
	for rows.Next() {
		var e api.Entry
		var tagsJSON string
		if err := rows.Scan(&e.ID, &e.Version, &e.Title, &e.Body, &tagsJSON, &e.CreatedAt, &e.UpdatedAt, &e.Namespace); err != nil {
			return nil, api.Page{}, err
		}
		_ = json.Unmarshal([]byte(tagsJSON), &e.Tags)
		out = append(out, e)
	}
	return out, api.Page{}, nil
}

func (s *sqliteStore) Search(ctx context.Context, q api.SearchQuery) ([]api.Entry, api.Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 500
	}
	// FTS table holds title/body/tags and unindexed namespace/id columns
	// Non-regex: use MATCH directly; Regex: narrow via token then filter in Go.
	var ids []string
	if !q.Regex {
		var rows *sql.Rows
		var err error
		if q.Namespace != "" {
			rows, err = s.db.QueryContext(ctx, `SELECT id FROM entries_fts WHERE namespace=? AND entries_fts MATCH ? LIMIT ?`, q.Namespace, q.Query, limit)
		} else {
			rows, err = s.db.QueryContext(ctx, `SELECT id FROM entries_fts WHERE entries_fts MATCH ? LIMIT ?`, q.Query, limit)
		}
		if err != nil {
			return nil, api.Page{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, api.Page{}, err
			}
			ids = append(ids, id)
		}
		return s.fetchEntriesByIDs(ctx, ids)
	}
	// Regex path with narrowing via longest word token
	token := longestWord(q.Query)
	var rows *sql.Rows
	var err error
	if token != "" {
		if q.Namespace != "" {
			rows, err = s.db.QueryContext(ctx, `SELECT id FROM entries_fts WHERE namespace=? AND entries_fts MATCH ? LIMIT ?`, q.Namespace, token, limit)
		} else {
			rows, err = s.db.QueryContext(ctx, `SELECT id FROM entries_fts WHERE entries_fts MATCH ? LIMIT ?`, token, limit)
		}
		if err != nil {
			return nil, api.Page{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, api.Page{}, err
			}
			ids = append(ids, id)
		}
	} else {
		// fallback: list latest entries in namespace (bounded)
		ents, _, err := s.ListEntries(ctx, api.ListQuery{Namespace: q.Namespace, Limit: limit})
		if err != nil {
			return nil, api.Page{}, err
		}
		for _, e := range ents {
			ids = append(ids, e.ID)
		}
	}
	// Fetch and regex filter in Go
	ents, _, err := s.fetchEntriesByIDs(ctx, ids)
	if err != nil {
		return nil, api.Page{}, err
	}
	re, err := compileRegex(q.Query)
	if err != nil {
		return nil, api.Page{}, err
	}
	out := make([]api.Entry, 0, len(ents))
	for _, e := range ents {
		hay := e.Title + "\n" + e.Body + "\n" + strings.Join(e.Tags, ",")
		if re.MatchString(hay) {
			out = append(out, e)
		}
	}
	return out, api.Page{}, nil
}

func (s *sqliteStore) fetchEntriesByIDs(ctx context.Context, ids []string) ([]api.Entry, api.Page, error) {
	out := make([]api.Entry, 0, len(ids))
	for _, id := range ids {
		e, err := s.GetEntry(ctx, id)
		if err == nil {
			out = append(out, e)
		}
	}
	return out, api.Page{}, nil
}

func longestWord(s string) string {
	s = strings.ToLower(s)
	best := ""
	run := []rune{}
	flush := func() {
		if len(run) >= 3 {
			w := string(run)
			if len(w) > len(best) {
				best = w
			}
		}
		run = run[:0]
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			run = append(run, r)
		} else {
			flush()
		}
	}
	flush()
	return best
}

func compileRegex(p string) (*regexp.Regexp, error) { return regexp.Compile(p) }

// openSQLite connects to a SQLite database using modernc.org/sqlite driver and ensures schema exists.
func openSQLite(ctx context.Context, dsn string) (*Store, io.Closer, error) {
	path := strings.TrimPrefix(dsn, "sqlite://")
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	dbh, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, err
	}
	if _, err := dbh.ExecContext(ctx, `PRAGMA journal_mode=WAL;`); err != nil {
		_ = dbh.Close()
		return nil, nil, err
	}
	if err := migrate(ctx, dbh); err != nil {
		_ = dbh.Close()
		return nil, nil, err
	}
	s := &sqliteStore{db: dbh}
	st := &Store{Events: s, Entries: s}
	return st, dbh, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS entries (
  id TEXT PRIMARY KEY,
  version INTEGER NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  tags TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL,
  namespace TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_entries_ns_created ON entries(namespace, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_entries_title ON entries(title);
CREATE TABLE IF NOT EXISTS events (
  time TIMESTAMP NOT NULL,
  type TEXT NOT NULL,
  id TEXT NOT NULL,
  entry_json BLOB
);
CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
  title, body, tags,
  namespace UNINDEXED, id UNINDEXED,
  tokenize='unicode61'
);
`)
	return err
}
