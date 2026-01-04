package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// filter and prefilter help compose a reusable CTE for namespace/time/tag constraints.
type filter struct {
	Namespace string
	Since     time.Time
	Until     time.Time
	Any       []string
	All       []string
	Limit     int
}

type prefilter struct {
	CTE  string
	Args []any
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// buildPrefilter returns a CTE named "filtered" that selects candidate note ids
// with created_at as c_at, applying namespace/time filters and prefiltering tags
// using an IN-list. Callers may add HAVING with precise Any/All logic if needed.
func buildPrefilter(f filter) prefilter {
	args := []any{}
	sql := "WITH filtered AS (\n  SELECT e.id, e.created_at AS c_at\n  FROM entries e"
	all := uniqueStrings(f.All)
	any := uniqueStrings(f.Any)
	if len(all)+len(any) > 0 {
		sql += "\n  JOIN note_tags nt ON nt.note_id = e.id"
	}
	conds := []string{}
	if f.Namespace != "" {
		conds = append(conds, "e.namespace = ?")
		args = append(args, f.Namespace)
	}
	if !f.Since.IsZero() {
		conds = append(conds, "e.created_at >= ?")
		args = append(args, f.Since.UTC())
	}
	if !f.Until.IsZero() {
		conds = append(conds, "e.created_at <= ?")
		args = append(args, f.Until.UTC())
	}
	if len(conds) > 0 {
		sql += "\n  WHERE " + strings.Join(conds, " AND ")
	}
	sql += "\n  GROUP BY e.id"
	hav := []string{}
	if l := len(all); l > 0 {
		ph := make([]string, 0, l)
		for range all {
			ph = append(ph, "?")
		}
		hav = append(hav, "SUM(CASE WHEN nt.tag IN ("+strings.Join(ph, ",")+") THEN 1 ELSE 0 END) = "+itoa(l))
		for _, t := range all {
			args = append(args, t)
		}
	}
	if l := len(any); l > 0 {
		ph := make([]string, 0, l)
		for range any {
			ph = append(ph, "?")
		}
		hav = append(hav, "SUM(CASE WHEN nt.tag IN ("+strings.Join(ph, ",")+") THEN 1 ELSE 0 END) >= 1")
		for _, t := range any {
			args = append(args, t)
		}
	}
	if len(hav) > 0 {
		sql += "\n  HAVING " + strings.Join(hav, " AND ")
	}
	sql += "\n)\n"
	return prefilter{CTE: sql, Args: args}
}

// EventLog
func (s *sqliteStore) Append(ctx context.Context, ev api.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := appendEventTx(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) List(ctx context.Context, cur api.Cursor, limit int) ([]api.Event, api.Cursor, error) {
	// Apply simple cursor and limit.
	q := `SELECT time, type, id, namespace, payload_type, payload FROM events`
	args := []any{}
	if !cur.After.IsZero() {
		q += ` WHERE time > ?`
		args = append(args, cur.After.UTC())
	}
	q += ` ORDER BY time ASC`
	limit = 2000
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, api.Cursor{}, err
	}
	defer rows.Close()
	var out []api.Event
	for rows.Next() {
		var t time.Time
		var typ string
		var id string
		var ns sql.NullString
		var payloadType sql.NullString
		var payload []byte
		if err := rows.Scan(&t, &typ, &id, &ns, &payloadType, &payload); err != nil {
			return nil, api.Cursor{}, err
		}
		out = append(out, api.Event{
			Time:        t,
			Type:        api.EventType(typ),
			ID:          id,
			Namespace:   ns.String,
			PayloadType: payloadType.String,
			Payload:     payload,
		})
	}
	var next api.Cursor
	if len(out) > 0 {
		next.After = out[len(out)-1].Time
	}
	return out, next, nil
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
	defer tx.Rollback()

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
	if shouldLog(ctx) {
		if err = appendEventTx(ctx, tx, api.Event{Time: time.Now().UTC(), Type: api.EventUpsert, ID: e.ID, Entry: &e}); err != nil {
			return api.Entry{}, err
		}
	}
	// FTS index
	if _, err = tx.ExecContext(ctx, `INSERT INTO entries_fts(rowid, title, body, tags, namespace, id) VALUES((SELECT rowid FROM entries WHERE id=?), ?, ?, ?, ?, ?)`, e.ID, e.Title, e.Body, tagsTokens, e.Namespace, e.ID); err != nil {
		return api.Entry{}, err
	}
	if err := tx.Commit(); err != nil {
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
	defer tx.Rollback()

	// Update using explicit version from 'e'
	res, err := tx.ExecContext(ctx, `UPDATE entries SET version=?, title=?, body=?, tags=?, updated_at=?, namespace=? WHERE id=? AND version=?`,
		e.Version, e.Title, e.Body, string(tagsJSON), e.UpdatedAt.UTC(), e.Namespace, e.ID, ifVersion)
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
	if shouldLog(ctx) {
		if err = appendEventTx(ctx, tx, api.Event{Time: time.Now().UTC(), Type: api.EventUpsert, ID: ne.ID, Entry: &ne}); err != nil {
			return api.Entry{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return api.Entry{}, err
	}
	return ne, nil
}

func (s *sqliteStore) DeleteEntry(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ns string
	if err := tx.QueryRowContext(ctx, `SELECT namespace FROM entries WHERE id=?`, id).Scan(&ns); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
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
	if shouldLog(ctx) {
		if err = appendEventTx(ctx, tx, api.Event{Time: time.Now().UTC(), Type: api.EventDelete, ID: id, Namespace: ns}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqliteStore) DeleteNamespace(ctx context.Context, namespace string) (int64, error) {
	if strings.TrimSpace(namespace) == "" {
		return 0, fmt.Errorf("namespace is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id FROM entries WHERE namespace=?`, namespace)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	_ = rows.Close()

	if _, err := tx.ExecContext(ctx, `DELETE FROM entries_fts WHERE namespace=?`, namespace); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE namespace=?`, namespace)
	if err != nil {
		return 0, err
	}
	deleted, _ := res.RowsAffected()

	if shouldLog(ctx) {
		for _, id := range ids {
			if err := appendEventTx(ctx, tx, api.Event{Time: time.Now().UTC(), Type: api.EventDelete, ID: id, Namespace: namespace}); err != nil {
				return 0, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// ListEntries retrieves entries based on provided filters, including namespace, time ranges, and tags.
// Note: By default, this summary listing does not load the entry body; set IncludeBody to include it.
func (s *sqliteStore) ListEntries(ctx context.Context, q api.ListQuery) ([]api.Entry, api.Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	bodySelect := "'' AS body"
	if q.IncludeBody {
		bodySelect = "e.body"
	}
	pf := buildPrefilter(filter{
		Namespace: q.Namespace,
		Since:     q.Since,
		Until:     q.Until,
		Any:       q.Any,
		All:       q.All,
	})
	cursor, hasCursor := parseCursorToken(q.Cursor)
	cursorClause, cursorArgs := cursorWhereClause(cursor, hasCursor, q.Reverse)
	orderClause := orderByClause(q.Reverse)
	pageLimit := limit + 1
	sqlq := pf.CTE + `SELECT e.id, e.version, e.title, ` + bodySelect + `, e.tags, e.created_at, e.updated_at, e.namespace
FROM filtered f
JOIN entries e ON e.id = f.id
` + cursorClause + `
` + orderClause + `
LIMIT ?`
	args := append(pf.Args, cursorArgs...)
	args = append(args, pageLimit)

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
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	if q.Reverse {
		reverseEntries(out)
	}
	page := buildPage(out, hasMore, q.Reverse, q.Cursor != "")
	return out, page, nil
}
func (s *sqliteStore) Search(ctx context.Context, q api.SearchQuery) ([]api.Entry, api.Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 500
	}
	var ids []string
	var hasMore bool
	var err error
	if q.Regex {
		ids, hasMore, err = s.searchRegex(ctx, q, limit)
	} else {
		ids, hasMore, err = s.searchFTS(ctx, q, limit)
	}
	if err != nil {
		return nil, api.Page{}, err
	}
	entries, _, err := s.fetchEntriesByIDs(ctx, ids)
	if err != nil {
		return nil, api.Page{}, err
	}
	if q.Reverse {
		reverseEntries(entries)
	}
	page := buildPage(entries, hasMore, q.Reverse, q.Cursor != "")
	return entries, page, nil
}

func (s *sqliteStore) searchFTS(ctx context.Context, q api.SearchQuery, limit int) ([]string, bool, error) {
	pf := buildPrefilter(filter{Namespace: q.Namespace, Since: q.Since, Until: q.Until, Any: q.Any, All: q.All})
	cursor, hasCursor := parseCursorToken(q.Cursor)
	cursorClause, cursorArgs := cursorWhereClause(cursor, hasCursor, q.Reverse)
	orderClause := orderByClause(q.Reverse)
	pageLimit := limit + 1
	sqlq := pf.CTE + `SELECT e.id
FROM filtered f
JOIN entries e ON e.id = f.id
JOIN entries_fts x ON x.id = e.id
`
	// If tag constraints exist, enforce precise Any/All via HAVING over a fresh tag join.
	needTags := len(q.All) > 0 || len(q.Any) > 0
	if needTags {
		sqlq += "JOIN note_tags nt2 ON nt2.note_id = e.id\n"
	}
	sqlq += "WHERE x.entries_fts MATCH ?\n"
	args := append([]any{}, pf.Args...)
	args = append(args, q.Query)
	if cursorClause != "" {
		sqlq += "AND " + strings.TrimPrefix(cursorClause, "WHERE ") + "\n"
		args = append(args, cursorArgs...)
	}
	if needTags {
		sqlq += "GROUP BY e.id\nHAVING "
		hav := []string{}
		if l := len(q.All); l > 0 {
			ph := make([]string, 0, l)
			for range q.All {
				ph = append(ph, "?")
			}
			hav = append(hav, "SUM(CASE WHEN nt2.tag IN ("+strings.Join(ph, ",")+") THEN 1 ELSE 0 END) = "+itoa(l))
			for _, t := range q.All {
				args = append(args, t)
			}
		}
		if l := len(q.Any); l > 0 {
			ph := make([]string, 0, l)
			for range q.Any {
				ph = append(ph, "?")
			}
			hav = append(hav, "SUM(CASE WHEN nt2.tag IN ("+strings.Join(ph, ",")+") THEN 1 ELSE 0 END) >= 1")
			for _, t := range q.Any {
				args = append(args, t)
			}
		}
		sqlq += strings.Join(hav, " AND ") + "\n"
	}
	sqlq += orderClause + "\nLIMIT ?"
	args = append(args, pageLimit)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	return ids, hasMore, nil
}

func (s *sqliteStore) searchRegex(ctx context.Context, q api.SearchQuery, limit int) ([]string, bool, error) {
	token := longestWord(q.Query)
	pf := buildPrefilter(filter{Namespace: q.Namespace, Since: q.Since, Until: q.Until, Any: q.Any, All: q.All})
	// Candidate cap to keep resource usage bounded
	cand := limit * 20
	if cand < limit {
		cand = limit
	}
	cursor, hasCursor := parseCursorToken(q.Cursor)
	cursorClause, cursorArgs := cursorWhereClause(cursor, hasCursor, q.Reverse)
	orderClause := orderByClause(q.Reverse)
	sqlq := pf.CTE + `SELECT e.id, e.title, e.body, e.tags
FROM filtered f
JOIN entries e ON e.id = f.id`
	args := append([]any{}, pf.Args...)
	if token != "" {
		sqlq += "\nJOIN entries_fts x ON x.id = e.id\nWHERE x.entries_fts MATCH ?"
		args = append(args, token)
	} else if cursorClause != "" {
		sqlq += "\n" + cursorClause
	}
	if token != "" && cursorClause != "" {
		sqlq += "\nAND " + strings.TrimPrefix(cursorClause, "WHERE ")
		args = append(args, cursorArgs...)
	} else if cursorClause != "" {
		args = append(args, cursorArgs...)
	}
	sqlq += "\n" + orderClause + "\nLIMIT ?"
	args = append(args, cand)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	re, err := compileRegex(q.Query)
	if err != nil {
		return nil, false, err
	}
	out := make([]string, 0, limit+1)
	for rows.Next() {
		var id, title, body, tagsJSON string
		if err := rows.Scan(&id, &title, &body, &tagsJSON); err != nil {
			return nil, false, err
		}
		var tags []string
		_ = json.Unmarshal([]byte(tagsJSON), &tags)
		hay := title + "\n" + body + "\n" + strings.Join(tags, ",")
		if re.MatchString(hay) {
			out = append(out, id)
			if len(out) >= limit+1 {
				break
			}
		}
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
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

func (s *sqliteStore) ListNamespaces(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT namespace FROM entries ORDER BY namespace ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ns string
		if err := rows.Scan(&ns); err != nil {
			return nil, err
		}
		if ns != "" {
			out = append(out, ns)
		}
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

type cursorToken struct {
	ts time.Time
	id string
}

func parseCursorToken(s string) (cursorToken, bool) {
	parts := strings.SplitN(strings.TrimSpace(s), "|", 2)
	if len(parts) != 2 {
		return cursorToken{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return cursorToken{}, false
	}
	id := strings.TrimSpace(parts[1])
	if id == "" {
		return cursorToken{}, false
	}
	return cursorToken{ts: ts, id: id}, true
}

func encodeCursorToken(e api.Entry) string {
	return fmt.Sprintf("%s|%s", e.CreatedAt.UTC().Format(time.RFC3339Nano), e.ID)
}

func cursorWhereClause(c cursorToken, ok bool, reverse bool) (string, []any) {
	if !ok {
		return "", nil
	}
	if reverse {
		return "WHERE (f.c_at > ? OR (f.c_at = ? AND e.id > ?))", []any{c.ts.UTC(), c.ts.UTC(), c.id}
	}
	return "WHERE (f.c_at < ? OR (f.c_at = ? AND e.id < ?))", []any{c.ts.UTC(), c.ts.UTC(), c.id}
}

func orderByClause(reverse bool) string {
	if reverse {
		return "ORDER BY f.c_at ASC, e.id ASC"
	}
	return "ORDER BY f.c_at DESC, e.id DESC"
}

func reverseEntries(entries []api.Entry) {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
}

func buildPage(entries []api.Entry, hasMore bool, reverse bool, hasCursor bool) api.Page {
	var page api.Page
	if len(entries) == 0 {
		return page
	}
	first := entries[0]
	last := entries[len(entries)-1]
	if reverse {
		page.Next = encodeCursorToken(last)
		if hasMore {
			page.Prev = encodeCursorToken(first)
		}
		return page
	}
	if hasMore {
		page.Next = encodeCursorToken(last)
	}
	if hasCursor {
		page.Prev = encodeCursorToken(first)
	}
	return page
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
	// set WAL mode
	if _, err := dbh.ExecContext(ctx, `PRAGMA journal_mode=WAL;`); err != nil {
		_ = dbh.Close()
		return nil, nil, err
	}
	// enforce foreign keys
	if _, err := dbh.ExecContext(ctx, `PRAGMA foreign_keys=ON;`); err != nil {
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
CREATE INDEX IF NOT EXISTS idx_entries_ns_created_id ON entries(namespace, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_entries_title ON entries(title);
CREATE TABLE IF NOT EXISTS events (
  time TIMESTAMP NOT NULL,
  type TEXT NOT NULL,
  id TEXT NOT NULL,
  namespace TEXT,
  payload_type TEXT,
  payload BLOB
);
-- Tags projection and metadata
CREATE TABLE IF NOT EXISTS note_tags (
  note_id TEXT NOT NULL,
  tag TEXT NOT NULL COLLATE NOCASE,
  PRIMARY KEY(note_id, tag),
  FOREIGN KEY(note_id) REFERENCES entries(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_note_tags_tag_note ON note_tags(tag, note_id);
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
	var err error
	ns := ev.Namespace
	if ns == "" && ev.Entry != nil {
		ns = ev.Entry.Namespace
	}
	payloadType := ev.PayloadType
	payload := ev.Payload
	if payloadType == "" && len(payload) == 0 {
		switch ev.Type {
		case api.EventUpsert:
			if ev.Entry != nil {
				payloadType = "plain_v1"
				payload, err = json.Marshal(ev.Entry)
				if err != nil {
					return err
				}
			}
		case api.EventDelete:
			payloadType = "plain_v1"
			dp := struct {
				ID        string `json:"id"`
				Namespace string `json:"namespace"`
			}{ID: ev.ID, Namespace: ns}
			payload, err = json.Marshal(dp)
			if err != nil {
				return err
			}
		}
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO events(time, type, id, namespace, payload_type, payload) VALUES(?,?,?,?,?,?)`, ev.Time.UTC(), string(ev.Type), ev.ID, ns, payloadType, payload)
	return err
}

func upsertNoteTags(ctx context.Context, tx *sql.Tx, noteID string, tags []string) error {
	for _, t := range tags {
		tt := strings.ToLower(strings.TrimSpace(t))
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
