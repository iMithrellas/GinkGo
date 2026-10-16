package ipc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/pkg/api"
)

func TestEntryProvenanceProtobufRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 123456789, time.UTC)
	base := api.Entry{ID: "note", Version: 2, Body: "clean body", CreatedAt: now, UpdatedAt: now}
	for _, p := range []*api.Provenance{nil, {}, {Sources: []api.Source{
		{ID: "voice-1", CapturedAt: now, Kind: "voice", OriginalText: "exact\ntranscript", Language: "en", STTEngine: "whisper.cpp", STTModel: "base"},
		{ID: "text-2", CapturedAt: now.Add(time.Minute), Kind: "text", OriginalText: "typed original"},
	}}} {
		e := base
		e.Provenance = p
		message := toPbEntry(e)
		data, err := proto.Marshal(&message)
		require.NoError(t, err)
		var decoded pb.Entry
		require.NoError(t, proto.Unmarshal(data, &decoded))
		got := fromPbEntry(&decoded)
		require.Equal(t, e, *got)
		require.Equal(t, e.Hash(), got.Hash())
	}
}
