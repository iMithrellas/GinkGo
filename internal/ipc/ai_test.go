package ipc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/pkg/api"
)

func TestAINoteRequestTranslation(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Minute)
	n := &pb.AINote{Text: " exact\ntext\n", Namespace: "work", Title: "title", Action: "expand", NoteId: "target", Provider: "test", Tags: []string{"tag"}, Deadline: timestamppb.New(deadline)}
	h := PBHandler(func(m Message) Response {
		require.Equal(t, "ai.note", m.Name)
		require.Equal(t, n.Text, m.Text)
		require.Equal(t, n.Namespace, m.Namespace)
		require.Equal(t, n.Title, m.Title)
		require.Equal(t, n.Action, m.Action)
		require.Equal(t, n.NoteId, m.NoteID)
		require.Equal(t, n.Provider, m.Provider)
		require.Equal(t, n.Tags, m.Tags)
		require.True(t, deadline.Equal(m.Deadline))
		return Response{OK: true, Entry: &api.Entry{ID: "saved"}, Action: "create", FallbackReason: "unavailable"}
	})
	r, err := h.Handle(context.Background(), &pb.Request{Cmd: &pb.Request_AiNote{AiNote: n}})
	require.NoError(t, err)
	response := r.(*pb.Response)
	require.Equal(t, "create", response.Action)
	require.Equal(t, "unavailable", response.FallbackReason)
	require.Equal(t, "saved", response.Entry.Id)
}

func TestNoteRequestProvenanceTranslation(t *testing.T) {
	p := &api.Provenance{Sources: []api.Source{{ID: "original", Kind: "text", OriginalText: "exact\ntext", CapturedAt: time.Now().UTC()}}}
	entry := toPbEntry(api.Entry{Provenance: p})
	h := PBHandler(func(m Message) Response {
		require.Equal(t, p, m.Provenance)
		return Response{OK: true}
	})
	for _, request := range []*pb.Request{
		{Cmd: &pb.Request_NoteAdd{NoteAdd: &pb.NoteAdd{Provenance: entry.Provenance}}},
		{Cmd: &pb.Request_NoteEdit{NoteEdit: &pb.NoteEdit{Provenance: entry.Provenance}}},
	} {
		_, err := h.Handle(context.Background(), request)
		require.NoError(t, err)
	}
}
