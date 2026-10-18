package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/pkg/api"
)

func TestImageReferencesImmutableAcrossEditAndReplication(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "images.db"))
	require.NoError(t, err)
	defer store.Close()
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	entry, err := store.Entries.CreateEntry(ctx, api.Entry{
		ID:        "note",
		Version:   1,
		Namespace: "work",
		Title:     "title",
		Body:      "body",
		CreatedAt: at,
		UpdatedAt: at,
		Provenance: &api.Provenance{Sources: []api.Source{{
			ID:           "source",
			Kind:         "text",
			CapturedAt:   at,
			OriginalText: "original",
			Images: []api.ImageAttachment{{
				ID:         "attachment-0123456789",
				SHA256:     "hash",
				MediaType:  "image/png",
				Size:       64,
				Width:      2,
				Height:     2,
				CapturedAt: at,
			}},
		}}},
	})
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		mutate func(*api.ImageAttachment)
	}{
		{"id", func(ref *api.ImageAttachment) { ref.ID = "another-attachment" }},
		{"hash", func(ref *api.ImageAttachment) { ref.SHA256 = "changed" }},
		{"dimensions", func(ref *api.ImageAttachment) { ref.Width++ }},
		{"capture time", func(ref *api.ImageAttachment) { ref.CapturedAt = ref.CapturedAt.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := store.Entries.GetEntry(ctx, entry.ID)
			require.NoError(t, err)
			tc.mutate(&changed.Provenance.Sources[0].Images[0])
			changed.Version++
			require.NotEqual(t, entry.Hash(), changed.Hash())
			_, err = store.Entries.UpdateEntryCAS(ctx, changed, entry.Version)
			require.ErrorIs(t, err, ErrConflict)
			err = store.ApplyReplication(ctx, api.Event{Type: api.EventUpsert, Entry: &changed})
			require.ErrorIs(t, err, ErrConflict)
		})
	}
}
