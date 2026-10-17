package cli

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/voiceevents"
)

type watchWriter struct{ output chan []byte }

func (w watchWriter) Write(b []byte) (int, error) {
	w.output <- append([]byte(nil), b...)
	return len(b), nil
}

func TestVoiceWatchCLI(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp/opencode", "voice-watch-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	path, err := voiceevents.SocketPath()
	require.NoError(t, err)
	h, err := voiceevents.NewHub(voiceevents.Options{})
	require.NoError(t, err)
	defer h.Close()
	server, err := voiceevents.Listen(context.Background(), path, h)
	require.NoError(t, err)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"watch", "--output", "json"})
	output := make(chan []byte, 2)
	cmd.SetOut(watchWriter{output})
	finished := make(chan error, 1)
	go func() { finished <- cmd.Execute() }()
	select {
	case b := <-output:
		var e voiceevents.Event
		require.NoError(t, json.Unmarshal(b, &e))
		require.Equal(t, "state", e.Type)
		require.False(t, e.Listening)
		require.False(t, e.RecordingNote)
	case <-time.After(time.Second):
		t.Fatal("no snapshot")
	}
	cancel()
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("watch did not cancel")
	}
}

func TestVoiceWatchValidationAndMissingSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	for _, args := range [][]string{{"watch", "--output", "text"}, {"watch", "unexpected"}, {"watch", "--output", "json"}} {
		cmd := newVoiceCmd()
		cmd.SetArgs(args)
		err := cmd.Execute()
		require.Error(t, err)
		if len(args) == 3 && args[2] == "json" {
			require.Contains(t, err.Error(), "connect voice event socket")
		}
	}
	cmd := newVoiceWatchCmd()
	require.Nil(t, cmd.Flags().Lookup("timeout"))
}
