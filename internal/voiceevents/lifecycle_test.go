package voiceevents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServerWaitReportsUnexpectedAcceptFailure(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	hub, err := NewHub(Options{})
	require.NoError(t, err)
	defer hub.Close()
	server, err := Listen(context.Background(), filepath.Join(dir, "voice.sock"), hub)
	require.NoError(t, err)
	require.NoError(t, server.listener.Close())
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "accept voice events")
	case <-time.After(time.Second):
		t.Fatal("Wait did not join failed endpoint")
	}
	require.ErrorContains(t, server.Close(), "accept voice events")
}
