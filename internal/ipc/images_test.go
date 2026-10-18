package ipc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/pkg/api"
)

func TestImageReferencesRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	entry := api.Entry{
		ID: "note",
		Provenance: &api.Provenance{Sources: []api.Source{{
			ID:           "original",
			CapturedAt:   at,
			Kind:         "voice",
			OriginalText: "this diagram",
			Images: []api.ImageAttachment{{
				ID:         "attachment-0123456789",
				SHA256:     "hash",
				MediaType:  "image/png",
				Size:       1024,
				Width:      1920,
				Height:     1080,
				CapturedAt: at,
				Label:      "DP-1",
			}},
		}}},
	}
	encoded := toPbEntry(entry)
	got := fromPbEntry(&encoded)
	require.Equal(t, entry.Provenance, got.Provenance)
	require.Equal(t, entry.Hash(), got.Hash())
}
