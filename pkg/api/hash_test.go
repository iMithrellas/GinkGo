package api

import (
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zeebo/blake3"
)

func TestEntryHashLegacyNilProvenance(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 123, time.UTC)
	e := Entry{ID: "note", Title: "title", Body: "body", Tags: []string{"b", "a"}, Namespace: "personal", CreatedAt: now, UpdatedAt: now}
	// Exact historical byte sequence: no new field marker for nil provenance.
	oldBytes := "note\x00title\x00body\x00a\x00b\x00\x00personal\x002026-10-16T12:00:00.000000123Z\x002026-10-16T12:00:00.000000123Z"
	sum := blake3.Sum256([]byte(oldBytes))
	require.Equal(t, hex.EncodeToString(sum[:]), e.Hash())
	data, err := json.Marshal(e)
	require.NoError(t, err)
	require.NotContains(t, string(data), "provenance")
}

func TestEntryHashProvenance(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 123, time.UTC)
	source := Source{ID: "source", CapturedAt: now, Kind: "voice", OriginalText: "original\x00text", Language: "en", STTEngine: "whisper.cpp", STTModel: "base"}
	e := Entry{ID: "note", Provenance: &Provenance{Sources: []Source{source}}}
	nilEntry := e
	nilEntry.Provenance = nil
	require.NotEqual(t, nilEntry.Hash(), e.Hash())
	for _, mutate := range []func(*Source){
		func(s *Source) { s.ID += "x" },
		func(s *Source) { s.CapturedAt = s.CapturedAt.Add(time.Nanosecond) },
		func(s *Source) { s.Kind = "text" },
		func(s *Source) { s.OriginalText += "x" },
		func(s *Source) { s.Language = "fr" },
		func(s *Source) { s.STTEngine = "other" },
		func(s *Source) { s.STTModel = "small" },
	} {
		changed := source
		mutate(&changed)
		other := Entry{ID: e.ID, Provenance: &Provenance{Sources: []Source{changed}}}
		require.NotEqual(t, e.Hash(), other.Hash())
	}
	zoneSource := source
	zoneSource.CapturedAt = now.In(time.FixedZone("offset", 3600))
	other := Entry{ID: e.ID, Provenance: &Provenance{Sources: []Source{zoneSource}}}
	require.Equal(t, e.Hash(), other.Hash())

	second := source
	second.ID = "second"
	ordered := Entry{Provenance: &Provenance{Sources: []Source{source, second}}}
	reversed := Entry{Provenance: &Provenance{Sources: []Source{second, source}}}
	require.NotEqual(t, ordered.Hash(), reversed.Hash())
	var decoded Entry
	data, err := json.Marshal(e)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, e.Hash(), decoded.Hash())
}

func TestEntry_Hash(t *testing.T) {
	now := time.Now().UTC()

	baseEntry := Entry{
		ID:        "test-id",
		Title:     "My Note",
		Body:      "Hello world",
		Tags:      []string{"work", "important"},
		Namespace: "personal",
		CreatedAt: now,
		UpdatedAt: now,
	}

	t.Run("identical entries produce identical hashes", func(t *testing.T) {
		e1 := baseEntry
		e2 := baseEntry
		assert.Equal(t, e1.Hash(), e2.Hash())
	})

	t.Run("tag order is deterministic", func(t *testing.T) {
		e1 := baseEntry
		e1.Tags = []string{"work", "important"}

		e2 := baseEntry
		e2.Tags = []string{"important", "work"}

		assert.Equal(t, e1.Hash(), e2.Hash(), "Hashes should match despite different tag order")
	})

	t.Run("case sensitivity in tags", func(t *testing.T) {
		e1 := baseEntry
		e1.Tags = []string{"WORK"}

		e2 := baseEntry
		e2.Tags = []string{"work"}

		assert.Equal(t, e1.Hash(), e2.Hash(), "Tags should be case-insensitive in hash")
	})

	t.Run("different content produces different hashes", func(t *testing.T) {
		e1 := baseEntry

		e2 := baseEntry
		e2.Title = "Different Title"

		e3 := baseEntry
		e3.Body = "Different body"

		assert.NotEqual(t, e1.Hash(), e2.Hash())
		assert.NotEqual(t, e1.Hash(), e3.Hash())
	})

	t.Run("timezone independence", func(t *testing.T) {
		loc, _ := time.LoadLocation("America/New_York")

		e1 := baseEntry
		e1.CreatedAt = now.In(loc)

		e2 := baseEntry
		e2.CreatedAt = now.UTC()

		assert.Equal(t, e1.Hash(), e2.Hash(), "Hash should be independent of timezone for the same instant")
	})

	t.Run("empty tags vs nil tags", func(t *testing.T) {
		e1 := baseEntry
		e1.Tags = []string{}

		e2 := baseEntry
		e2.Tags = nil

		assert.Equal(t, e1.Hash(), e2.Hash(), "Empty slice and nil slice should result in same hash")
	})
}
