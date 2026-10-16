package ipc

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/internal/ipc/transport"
)

type voiceListener struct{ net.Listener }

func (l voiceListener) Listen(context.Context) (net.Listener, error) { return l.Listener, nil }

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

func TestVoiceControlPreservesCaptureDurations(t *testing.T) {
	input := &pb.VoiceControl{Action: "listen", Namespace: "work", Save: true, Capture: &pb.CaptureOptions{Target: "mic", SilenceTimeout: -1500000000, MaxDuration: 120000000000, StartTimeout: 30000000000, Threshold: .02}}
	h := PBHandler(func(m Message) Response {
		require.Equal(t, "voice.listen", m.Name)
		require.True(t, m.Save)
		require.Equal(t, "mic", m.Capture.Target)
		require.Equal(t, -1500*time.Millisecond, m.Capture.SilenceTimeout)
		require.Equal(t, 2*time.Minute, m.Capture.MaxDuration)
		require.Equal(t, 30*time.Second, m.Capture.StartTimeout)
		return Response{OK: true, VoiceStatus: &VoiceStatus{Enabled: true, State: "listening", StartedAt: time.Unix(10, 0)}}
	})
	r, err := h.Handle(context.Background(), &pb.Request{Cmd: &pb.Request_VoiceControl{VoiceControl: input}})
	require.NoError(t, err)
	status := r.(*pb.Response).VoiceStatus
	require.True(t, status.Enabled)
	require.Equal(t, "listening", status.State)
	require.Equal(t, int64(10), status.StartedAt.AsTime().Unix())
}

func TestVoiceListenDeadlineRoundTripAndEnableIgnoresJobDeadline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ipc.sock")
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Message, 2)
	done := make(chan error, 1)
	go func() {
		done <- transport.NewUnixServer(voiceListener{l}).Serve(ctx, PBHandler(func(m Message) Response { got <- m; return Response{OK: true} }))
	}()
	deadline := time.Now().Add(time.Minute).UTC()
	reqCtx, stop := context.WithDeadline(context.Background(), deadline.Add(time.Minute))
	defer stop()
	_, err = Request(reqCtx, path, Message{Name: "voice.listen", Deadline: deadline})
	require.NoError(t, err)
	m := <-got
	require.WithinDuration(t, deadline, m.Deadline, time.Millisecond)
	_, err = Request(reqCtx, path, Message{Name: "voice.enable", Deadline: deadline})
	require.NoError(t, err)
	m = <-got
	require.True(t, m.Deadline.IsZero(), "enable must not pass a job lifetime deadline")
	cancel()
	require.NoError(t, <-done)
}

func TestVoiceControlRejectsInvalidDeadline(t *testing.T) {
	h := PBHandler(func(Message) Response { t.Fatal("invalid request reached handler"); return Response{} })
	got, err := h.Handle(context.Background(), &pb.Request{Cmd: &pb.Request_VoiceControl{VoiceControl: &pb.VoiceControl{Action: "listen", Deadline: &timestamppb.Timestamp{Seconds: 1<<63 - 1}}}})
	require.NoError(t, err)
	resp := got.(*pb.Response)
	require.False(t, resp.Ok)
	require.Equal(t, "invalid deadline", resp.Msg)
}
