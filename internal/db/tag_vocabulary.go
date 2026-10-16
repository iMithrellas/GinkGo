package db

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// TagVocabulary is an optional metadata-only capability. Implementations return
// at most 40 normalized names from exactly namespace, preferring relevant tags.
// No note bodies, descriptions or provenance are returned.
type TagVocabulary interface {
	RankedTags(ctx context.Context, namespace, input string, now time.Time, limit int) ([]string, error)
}

type vocabularyStat struct {
	name     string
	count    int
	latest   time.Time
	relevant bool
	score    float64
}

// tagFrecency combines distinct-note frequency and latest-note modification age.
// Future dates are clamped to age zero. A 30-day age halves the recency bonus.
func tagFrecency(count int, latest, now time.Time) float64 {
	age := math.Max(0, now.Sub(latest).Hours()/24)
	return math.Log2(1+float64(count)) + 1/(1+age/30)
}

func topicTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// RankedTags ranks metadata across the whole namespace, not just recent notes.
// Relevance (a whole topic token occurring in input) precedes frecency; ties use
// normalized name ascending. Frequency counts a note only once per normalized tag.
func (s *sqliteStore) RankedTags(ctx context.Context, namespace, input string, now time.Time, limit int) ([]string, error) {
	if limit <= 0 || limit > 40 {
		limit = 40
	}
	rows, err := s.db.QueryContext(ctx, `SELECT nt.tag, nt.note_id, e.updated_at FROM note_tags nt JOIN entries e ON e.id=nt.note_id WHERE e.namespace=?`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := map[string]*vocabularyStat{}
	seen := map[string]map[string]bool{}
	tokens := map[string]bool{}
	for _, token := range topicTokens(input) {
		tokens[token] = true
	}
	for rows.Next() {
		var name, id string
		var updated time.Time
		if err := rows.Scan(&name, &id, &updated); err != nil {
			return nil, err
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || len(name) > 128 {
			continue
		}
		stat := stats[name]
		if stat == nil {
			stat = &vocabularyStat{name: name}
			for _, token := range topicTokens(name) {
				stat.relevant = stat.relevant || tokens[token]
			}
			stats[name] = stat
			seen[name] = map[string]bool{}
		}
		if !seen[name][id] {
			stat.count++
			seen[name][id] = true
		}
		if updated.After(stat.latest) {
			stat.latest = updated
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ranked := make([]vocabularyStat, 0, len(stats))
	for _, stat := range stats {
		stat.score = tagFrecency(stat.count, stat.latest, now)
		ranked = append(ranked, *stat)
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.relevant != b.relevant {
			return a.relevant
		}
		if a.score != b.score {
			return a.score > b.score
		}
		return a.name < b.name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	result := make([]string, 0, len(ranked))
	for _, stat := range ranked {
		result = append(result, stat.name)
	}
	return result, ctx.Err()
}
