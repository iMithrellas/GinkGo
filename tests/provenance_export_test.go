package tests

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/present/format"
	"github.com/mithrel/ginkgo/pkg/api"
)

func TestProvenanceExportRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 123, time.UTC)
	e := api.Entry{ID: "note", Body: "clean body", CreatedAt: now, UpdatedAt: now,
		Provenance: &api.Provenance{Sources: []api.Source{
			{ID: "first", CapturedAt: now, Kind: "voice", OriginalText: "secretoriginalterm\nexact transcript", Language: "en", STTEngine: "whisper.cpp", STTModel: "base"},
			{ID: "second", CapturedAt: now.Add(time.Minute), Kind: "text", OriginalText: "typed original"},
		}}}
	for _, name := range []string{"json", "ndjson"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			var decoded []api.Entry
			if name == "json" {
				require.NoError(t, format.WriteJSONEntries(&buf, []api.Entry{e}, false))
				require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
			} else {
				require.NoError(t, format.WriteNDJSONEntries(&buf, []api.Entry{e}))
				var entry api.Entry
				require.NoError(t, json.NewDecoder(&buf).Decode(&entry))
				decoded = []api.Entry{entry}
			}
			require.Equal(t, []api.Entry{e}, decoded)
			require.Equal(t, e.Hash(), decoded[0].Hash())
		})
	}
	var rendered bytes.Buffer
	require.NoError(t, format.WritePrettyEntry(&rendered, e))
	text := ansi.Strip(rendered.String())
	require.Contains(t, text, "clean body")
	require.NotContains(t, text, "secretoriginalterm")
}
