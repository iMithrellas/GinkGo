package ipc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/ipc/pb"
)

func TestVoiceTranscribeTypedTranslation(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Minute)
	n := &pb.VoiceTranscribe{AudioPath: "/audio.wav", Save: true, Mode: "ai", Namespace: "work", Title: "override", Tags: []string{"tag"}, Provider: "test", Action: "expand", NoteId: "target", Model: "/model.bin", Language: "fr", Deadline: timestamppb.New(deadline)}
	h := PBHandler(func(m Message) Response {
		require.Equal(t, "voice.transcribe", m.Name)
		require.Equal(t, n.AudioPath, m.AudioPath)
		require.Equal(t, n.Save, m.Save)
		require.Equal(t, n.Mode, m.Mode)
		require.Equal(t, n.Namespace, m.Namespace)
		require.Equal(t, n.Title, m.Title)
		require.Equal(t, n.Tags, m.Tags)
		require.Equal(t, n.Provider, m.Provider)
		require.Equal(t, n.Action, m.Action)
		require.Equal(t, n.NoteId, m.NoteID)
		require.Equal(t, n.Model, m.Model)
		require.Equal(t, n.Language, m.Language)
		require.True(t, deadline.Equal(m.Deadline))
		return Response{OK: true, Transcript: &Transcript{Text: " exact\ntext ", Language: "fr", Engine: "whisper.cpp", Model: "/model.bin"}}
	})
	r, err := h.Handle(context.Background(), &pb.Request{Cmd: &pb.Request_VoiceTranscribe{VoiceTranscribe: n}})
	require.NoError(t, err)
	response := r.(*pb.Response)
	require.True(t, response.Ok)
	require.Equal(t, " exact\ntext ", response.Transcript.Text)
	require.Equal(t, "fr", response.Transcript.Language)
	require.Equal(t, "whisper.cpp", response.Transcript.Engine)
	require.Equal(t, "/model.bin", response.Transcript.Model)
}
