package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

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
	tagsTokens := strings.Join(e.Tags, " ")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Entry{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `INSERT INTO entries(id, version, title, body, tags, created_at, updated_at, namespace) VALUES(?,?,?,?,?,?,?,?)`,
		e.ID, e.Version, e.Title, e.Body, string(tagsJSON), e.CreatedAt.UTC(), e.UpdatedAt.UTC(), e.Namespace); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			err = ErrConflict
		}
		return api.Entry{}, err
	}
	// Note tags (case-insensitive tags)
	if err = upsertNoteTags(ctx, tx, e.ID, e.Tags); err != nil {
		return api.Entry{}, err
	}
	// Event
	if err = appendEventTx(ctx, tx, api.Event{Time: e.CreatedAt, Type: api.EventUpsert, ID: e.ID, Entry: &e}); err != nil {
		return api.Entry{}, err
	}
	// FTS index
	if _, err = tx.ExecContext(ctx, `INSERT INTO entries_fts(rowid, title, body, tags, namespace, id) VALUES((SELECT rowid FROM entries WHERE id=?), ?, ?, ?, ?, ?)`, e.ID, e.Title, e.Body, tagsTokens, e.Namespace, e.ID); err != nil {
		return api.Entry{}, err
	}
	if err = tx.Commit(); err != nil {
		return api.Entry{}, err
	}
	return e, nil
}

func (s *sqliteStore) UpdateEntryCAS(ctx context.Context, e api.Entry, ifVersion int64) (api.Entry, error) {
	tagsJSON, _ := json.Marshal(e.Tags)
	tagsTokens := strings.Join(e.Tags, " ")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Entry{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx, `UPDATE entries SET version=version+1, title=?, body=?, tags=?, updated_at=?, namespace=? WHERE id=? AND version=?`,
		e.Title, e.Body, string(tagsJSON), e.UpdatedAt.UTC(), e.Namespace, e.ID, ifVersion)
	if err != nil {
		return api.Entry{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return api.Entry{}, ErrConflict
	}

	// Refresh tags projection
	if _, err = tx.ExecContext(ctx, `DELETE FROM note_tags WHERE note_id=?`, e.ID); err != nil {
		return api.Entry{}, err
	}
	if err = upsertNoteTags(ctx, tx, e.ID, e.Tags); err != nil {
		return api.Entry{}, err
	}

	// Read back current entry
	var ne api.Entry
	var tagsJSONBack string
	row := tx.QueryRowContext(ctx, `SELECT id, version, title, body, tags, created_at, updated_at, namespace FROM entries WHERE id=?`, e.ID)
	if err = row.Scan(&ne.ID, &ne.Version, &ne.Title, &ne.Body, &tagsJSONBack, &ne.CreatedAt, &ne.UpdatedAt, &ne.Namespace); err != nil {
		return api.Entry{}, err
	}
	_ = json.Unmarshal([]byte(tagsJSONBack), &ne.Tags)

	// Refresh FTS
	if _, err = tx.ExecContext(ctx, `DELETE FROM entries_fts WHERE id=?`, ne.ID); err != nil {
		return api.Entry{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO entries_fts(rowid, title, body, tags, namespace, id) VALUES((SELECT rowid FROM entries WHERE id=?), ?, ?, ?, ?, ?)`, ne.ID, ne.Title, ne.Body, tagsTokens, ne.Namespace, ne.ID); err != nil {
		return api.Entry{}, err
	}
	// Append event
	if err = appendEventTx(ctx, tx, api.Event{Time: ne.UpdatedAt, Type: api.EventUpsert, ID: ne.ID, Entry: &ne}); err != nil {
		return api.Entry{}, err
	}
	if err = tx.Commit(); err != nil {
		return api.Entry{}, err
	}
	return ne, nil
}

func (s *sqliteStore) DeleteEntry(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	res, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM entries_fts WHERE id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM note_tags WHERE note_id=?`, id); err != nil {
		return err
	}
	if err = appendEventTx(ctx, tx, api.Event{Time: time.Now().UTC(), Type: api.EventDelete, ID: id}); err != nil {
		return err
	}
	return tx.Commit()
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
	var ids []string
	var err error
	if q.Regex {
		ids, err = s.searchRegex(ctx, q, limit)
	} else {
		ids, err = s.searchFTS(ctx, q, limit)
	}
	if err != nil {
		return nil, api.Page{}, err
	}
	return s.fetchEntriesByIDs(ctx, ids)
}

func (s *sqliteStore) searchFTS(ctx context.Context, q api.SearchQuery, limit int) ([]string, error) {
	if len(q.All) > 0 || len(q.Any) > 0 {
		return s.searchFTSWithTags(ctx, q, limit)
	}
	return s.searchFTSSimple(ctx, q, limit)
}

func (s *sqliteStore) searchFTSWithTags(ctx context.Context, q api.SearchQuery, limit int) ([]string, error) {
	args := []any{}
	cte := `WITH tagged AS (
  SELECT e.id, MAX(e.created_at) AS c_at
  FROM entries e
  JOIN note_tags nt ON nt.note_id = e.id`
	if q.Namespace != "" {
		cte += "\n  WHERE e.namespace = ?"
		args = append(args, q.Namespace)
	}
	cte += "\n  GROUP BY e.id\n  HAVING "
	havings := []string{}
	if len(q.All) > 0 {
		ph := strings.Repeat("?,", len(q.All))
		ph = strings.TrimSuffix(ph, ",")
		havings = append(havings, "SUM(CASE WHEN nt.tag IN ("+ph+") THEN 1 ELSE 0 END) = "+itoa(len(q.All)))
		for _, t := range q.All {
			args = append(args, t)
		}
	}
	if len(q.Any) > 0 {
		ph := strings.Repeat("?,", len(q.Any))
		ph = strings.TrimSuffix(ph, ",")
		havings = append(havings, "SUM(CASE WHEN nt.tag IN ("+ph+") THEN 1 ELSE 0 END) >= 1")
		for _, t := range q.Any {
			args = append(args, t)
		}
	}
	cte += strings.Join(havings, " AND ") + "\n)\n"
	sqlq := cte + `SELECT e.id
FROM tagged t
JOIN entries e ON e.id = t.id
JOIN entries_fts f ON f.rowid = e.rowid
WHERE f.entries_fts MATCH ?
ORDER BY t.c_at DESC
LIMIT ?`
	args = append(args, q.Query, limit)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *sqliteStore) searchFTSSimple(ctx context.Context, q api.SearchQuery, limit int) ([]string, error) {
	sqlq := `SELECT e.id
FROM entries_fts f
JOIN entries e ON e.id = f.id
WHERE f.entries_fts MATCH ?`
	args := []any{q.Query}
	if q.Namespace != "" {
		sqlq += " AND e.namespace = ?"
		args = append(args, q.Namespace)
	}
	sqlq += " ORDER BY e.created_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *sqliteStore) searchRegex(ctx context.Context, q api.SearchQuery, limit int) ([]string, error) {
	token := longestWord(q.Query)
	var rows *sql.Rows
	var err error
	ids := make([]string, 0, limit)
	// Optional tag candidate set
	var tagAllowed map[string]struct{}
	if len(q.All) > 0 || len(q.Any) > 0 {
		ents, _, terr := s.ListByTags(ctx, api.TagFilterQuery{Namespace: q.Namespace, Any: q.Any, All: q.All, Limit: 20000})
		if terr == nil {
			tagAllowed = make(map[string]struct{}, len(ents))
			for _, e := range ents {
				tagAllowed[e.ID] = struct{}{}
			}
		}
	}
	if token != "" {
		if q.Namespace != "" {
			rows, err = s.db.QueryContext(ctx, `SELECT id FROM entries_fts WHERE namespace=? AND entries_fts MATCH ? LIMIT ?`, q.Namespace, token, limit)
		} else {
			rows, err = s.db.QueryContext(ctx, `SELECT id FROM entries_fts WHERE entries_fts MATCH ? LIMIT ?`, token, limit)
		}
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			if tagAllowed != nil {
				if _, ok := tagAllowed[id]; ok {
					ids = append(ids, id)
				}
			} else {
				ids = append(ids, id)
			}
		}
	} else {
		// fallback: list latest entries in namespace (bounded)
		ents, _, err := s.ListEntries(ctx, api.ListQuery{Namespace: q.Namespace, Limit: limit})
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if tagAllowed != nil {
				if _, ok := tagAllowed[e.ID]; ok {
					ids = append(ids, e.ID)
				}
			} else {
				ids = append(ids, e.ID)
			}
		}
	}
	// Fetch and regex filter in Go
	ents, _, err := s.fetchEntriesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	re, err := compileRegex(q.Query)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		hay := e.Title + "\n" + e.Body + "\n" + strings.Join(e.Tags, ",")
		if re.MatchString(hay) {
			out = append(out, e.ID)
		}
	}
	return out, nil
}

// ListByTags implements filtering entries by tag sets (Any/All) with optional namespace.
func (s *sqliteStore) ListByTags(ctx context.Context, q api.TagFilterQuery) ([]api.Entry, api.Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	// Build dynamic SQL
	var args []any
	var where []string
	where = append(where, "e.id = nt.note_id")
	if q.Namespace != "" {
		where = append(where, "e.namespace = ?")
		args = append(args, q.Namespace)
	}
	// HAVING clauses using conditional counts for All/Any
	having := []string{}
	if len(q.All) > 0 {
		ph := make([]string, 0, len(q.All))
		for range q.All {
			ph = append(ph, "?")
		}
		having = append(having, "COUNT(DISTINCT CASE WHEN nt.tag IN ("+strings.Join(ph, ",")+") THEN nt.tag END) = "+itoa(len(q.All)))
		for _, t := range q.All {
			args = append(args, t)
		}
	}
	if len(q.Any) > 0 {
		ph := make([]string, 0, len(q.Any))
		for range q.Any {
			ph = append(ph, "?")
		}
		having = append(having, "COUNT(DISTINCT CASE WHEN nt.tag IN ("+strings.Join(ph, ",")+") THEN nt.tag END) >= 1")
		for _, t := range q.Any {
			args = append(args, t)
		}
	}
	sqlq := "SELECT e.id, e.version, e.title, e.body, e.tags, e.created_at, e.updated_at, e.namespace " +
		"FROM entries e JOIN note_tags nt ON e.id = nt.note_id"
	if len(where) > 0 {
		sqlq += " WHERE " + strings.Join(where, " AND ")
	}
	sqlq += " GROUP BY e.id"
	if len(having) > 0 {
		sqlq += " HAVING " + strings.Join(having, " AND ")
	}
	sqlq += " ORDER BY e.created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, sqlq, args...)
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

// ListTags returns tag counts (per namespace if provided) with optional prefix filter.
func (s *sqliteStore) ListTags(ctx context.Context, q api.TagsQuery) ([]api.TagStat, error) {
	var args []any
	sqlq := `SELECT nt.tag, COUNT(DISTINCT nt.note_id) as cnt, COALESCE(t.description, '')
             FROM note_tags nt
             JOIN entries e ON e.id = nt.note_id
             LEFT JOIN tags t ON t.tag = nt.tag`
	conds := []string{}
	if q.Namespace != "" {
		conds = append(conds, "e.namespace = ?")
		args = append(args, q.Namespace)
	}
	if q.Prefix != "" {
		conds = append(conds, "nt.tag LIKE ?")
		args = append(args, q.Prefix+"%")
	}
	if len(conds) > 0 {
		sqlq += " WHERE " + strings.Join(conds, " AND ")
	}
	sqlq += " GROUP BY nt.tag ORDER BY cnt DESC, nt.tag ASC"
	if q.Limit > 0 {
		sqlq += " LIMIT ?"
		args = append(args, q.Limit)
	}
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.TagStat
	for rows.Next() {
		var t api.TagStat
		if err := rows.Scan(&t.Tag, &t.Count, &t.Description); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
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
	run := make([]rune, 0, 32)

	flush := func() {
		if len(run) >= 3 {
			if len(run) > len(best) {
				best = string(run)
			}
		}
		run = run[:0]
	}

	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
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
-- Tags projection and metadata
CREATE TABLE IF NOT EXISTS note_tags (
  note_id TEXT NOT NULL,
  tag TEXT NOT NULL COLLATE NOCASE,
  PRIMARY KEY(note_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_note_tags_tag ON note_tags(tag, note_id);
CREATE TABLE IF NOT EXISTS tags (
  tag TEXT PRIMARY KEY COLLATE NOCASE,
  description TEXT DEFAULT ''
);
CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
  title, body, tags,
  namespace UNINDEXED, id UNINDEXED,
  tokenize='unicode61'
);
`)
	return err
}

// appendEventTx writes to events within the provided transaction.
func appendEventTx(ctx context.Context, tx *sql.Tx, ev api.Event) error {
	var entryJSON []byte
	if ev.Entry != nil {
		b, _ := json.Marshal(ev.Entry)
		entryJSON = b
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO events(time, type, id, entry_json) VALUES(?,?,?,?)`, ev.Time.UTC(), string(ev.Type), ev.ID, entryJSON)
	return err
}

func upsertNoteTags(ctx context.Context, tx *sql.Tx, noteID string, tags []string) error {
	for _, t := range tags {
		tt := strings.TrimSpace(t)
		if tt == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tags(tag) VALUES(?)`, tt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO note_tags(note_id, tag) VALUES(?,?)`, noteID, tt); err != nil {
			return err
		}
	}
	return nil
}

// helper to convert int to string without strconv import churn
func itoa(n int) string { return strconv.Itoa(n) }
