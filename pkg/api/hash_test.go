package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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
