package voiceevents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestHub(t *testing.T, options Options) *Hub {
	t.Helper()
	h, err := NewHub(options)
	require.NoError(t, err)
	t.Cleanup(h.Close)
	return h
}

func testSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp/opencode", "voice-events-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return filepath.Join(dir, "events.sock")
}

func startTestServer(t *testing.T, h *Hub) (string, *Server) {
	t.Helper()
	path := testSocket(t)
	s, err := Listen(context.Background(), path, h)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return path, s
}

func decodeEvent(t *testing.T, b []byte) Event {
	t.Helper()
	var e Event
	require.NoError(t, json.Unmarshal(b, &e))
	return e
}

func TestSnapshotAndDelayedAttribution(t *testing.T) {
	h := newTestHub(t, Options{})
	require.NoError(t, h.UpdateState(State{Listening: true, RecordingNote: true, ActiveNoteSessionID: "session"}))
	require.NoError(t, h.Publish(Event{Type: "transcript", UtteranceID: "u", Revision: 1, Text: "private history"}))
	s, err := h.subscribe()
	require.NoError(t, err)
	e := decodeEvent(t, <-s.queue)
	require.Equal(t, "state", e.Type)
	require.Empty(t, e.Text)
	require.Equal(t, uint64(2), e.Seq)
	require.True(t, e.RecordingNote)
	require.NoError(t, h.UpdateState(State{}))
	b := <-s.queue
	require.Contains(t, string(b), `"listening":false`)
	require.Contains(t, string(b), `"recording_note":false`)
	require.Contains(t, string(b), `"active_note_session_id":""`)
	require.NoError(t, h.Publish(Event{Type: "transcript", UtteranceID: "u", Revision: 2, Text: "done", Final: true, NoteSessionID: "session"}))
	e = decodeEvent(t, <-s.queue)
	require.False(t, e.RecordingNote)
	require.Equal(t, "session", e.NoteSessionID)
	require.True(t, e.Final)
}

func TestSnapshotPublishRaceOrdering(t *testing.T) {
	h := newTestHub(t, Options{QueueSize: 1024})
	for i := 0; i < 100; i++ {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = h.Publish(Event{Type: "state"}) }()
		s, err := h.subscribe()
		require.NoError(t, err)
		wg.Wait()
		first := decodeEvent(t, <-s.queue)
		require.Equal(t, "state", first.Type)
		select {
		case b := <-s.queue:
			require.Greater(t, decodeEvent(t, b).Seq, first.Seq)
		default:
		}
		h.unsubscribe(s)
	}
}

func TestSlowSubscriberIsolationAndLimit(t *testing.T) {
	h := newTestHub(t, Options{QueueSize: 1, MaxClients: 2})
	slow, err := h.subscribe()
	require.NoError(t, err)
	fast, err := h.subscribe()
	require.NoError(t, err)
	_, err = h.subscribe()
	require.Error(t, err)
	<-fast.queue
	require.NoError(t, h.Publish(Event{Type: "note.saved", NoteSessionID: "s", NoteID: "n"}))
	select {
	case <-slow.done:
	default:
		t.Fatal("overloaded subscriber still healthy")
	}
	e := decodeEvent(t, <-fast.queue)
	require.Equal(t, "note.saved", e.Type)
	for i := 0; i < 100; i++ {
		require.NoError(t, h.Publish(Event{Type: "state"}))
		<-fast.queue
	}
	h.Close()
	select {
	case <-fast.done:
	default:
		t.Fatal("close did not disconnect")
	}
	require.Error(t, h.Publish(Event{Type: "state"}))
}

func TestProtocolBounds(t *testing.T) {
	h := newTestHub(t, Options{})
	nan := math.NaN()
	negative := int64(-1)
	for _, event := range []Event{
		{Type: "unknown"}, {Type: "level", Level: &nan}, {Type: "level"}, {Type: "transcript", UtteranceID: "u"},
		{Type: "transcript", UtteranceID: "u", Revision: 1, Text: strings.Repeat("x", 48*1024+1)},
		{Type: "state", NoteID: strings.Repeat("x", 257)}, {Type: "state", Text: string([]byte{0xff})},
		{Type: "state", AudioStartMS: &negative}, {Type: "note.saved"}, {Type: "note.canceled"}, {Type: "note.error"}, {Type: "error"},
		{Type: "transcript", UtteranceID: "u", Revision: 1, Text: strings.Repeat("\x00", 48*1024)},
	} {
		require.Error(t, h.Publish(event), "%+v", event.Type)
	}
	for _, state := range []State{{RecordingNote: true}, {ActiveNoteSessionID: "s"}, {ActiveNoteSessionID: strings.Repeat("s", 257)}} {
		require.Error(t, h.UpdateState(state))
	}
	_, err := NewHub(Options{QueueSize: -1})
	require.Error(t, err)
	_, err = NewHub(Options{MaxClients: 257})
	require.Error(t, err)
	require.NoError(t, h.Publish(Event{Type: "transcript", UtteranceID: "u", Revision: 1, Text: strings.Repeat("x", 48*1024)}))
}

func TestWatchMultipleClientsAndCancellation(t *testing.T) {
	h := newTestHub(t, Options{})
	path, server := startTestServer(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	observations := make(chan Event, 4)
	for i := 0; i < 2; i++ {
		go func() { results <- Watch(ctx, path, func(e Event) error { observations <- e; return nil }) }()
	}
	for i := 0; i < 2; i++ {
		select {
		case e := <-observations:
			require.Equal(t, "state", e.Type)
		case <-time.After(time.Second):
			t.Fatal("missing snapshot")
		}
	}
	require.NoError(t, h.UpdateState(State{Listening: true}))
	for i := 0; i < 2; i++ {
		select {
		case e := <-observations:
			require.True(t, e.Listening)
		case <-time.After(time.Second):
			t.Fatal("missing state")
		}
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("watch cancellation blocked")
		}
	}
	require.NoError(t, server.Close())
	_, err := os.Lstat(path)
	require.True(t, os.IsNotExist(err))
}

func TestServerRejectsAnyInboundBytes(t *testing.T) {
	h := newTestHub(t, Options{})
	path, _ := startTestServer(t, h)
	for _, payload := range []string{"subscribe\n", strings.Repeat("x", MaxMessageBytes*2)} {
		conn, err := net.Dial("unix", path)
		require.NoError(t, err)
		require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
		_, _ = conn.Write([]byte(payload))
		buf := make([]byte, 4096)
		for {
			_, err = conn.Read(buf)
			if err != nil {
				break
			}
		}
		var timeout net.Error
		require.False(t, errors.As(err, &timeout) && timeout.Timeout(), "server did not reject input")
		conn.Close()
	}
}

func TestBlockedSocketDoesNotBlockPublishOrOtherClient(t *testing.T) {
	h := newTestHub(t, Options{QueueSize: 2})
	path, server := startTestServer(t, h)
	slow, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer slow.Close()
	// Read the snapshot, then stop draining this socket.
	var initial Event
	require.NoError(t, json.NewDecoder(slow).Decode(&initial))
	fast, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer fast.Close()
	decoder := json.NewDecoder(fast)
	require.NoError(t, decoder.Decode(&initial))
	for i := 0; i < 100; i++ {
		require.NoError(t, h.Publish(Event{Type: "transcript", UtteranceID: "u", Revision: uint64(i + 1), Text: strings.Repeat("x", 48*1024)}))
		require.NoError(t, fast.SetReadDeadline(time.Now().Add(time.Second)))
		var e Event
		require.NoError(t, decoder.Decode(&e))
	}
	closed := make(chan struct{})
	go func() { server.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked on socket")
	}
}

func TestSocketSafetyAndOwnership(t *testing.T) {
	h := newTestHub(t, Options{})
	path, server := startTestServer(t, h)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = Listen(context.Background(), path, h)
	require.Error(t, err)
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	conn.Close()
	// Cleanup must not unlink a replacement file.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("replacement"), 0o600))
	require.NoError(t, server.Close())
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "replacement", string(b))
	_, err = Listen(context.Background(), path, h)
	require.Error(t, err)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink("missing", path))
	_, err = Listen(context.Background(), path, h)
	require.Error(t, err)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o755))
	_, err = Listen(context.Background(), path, h)
	require.Error(t, err)
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o700))
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	stale.SetUnlinkOnClose(false)
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, stale.Close())
	recovered, err := Listen(context.Background(), path, h)
	require.NoError(t, err)
	require.NoError(t, recovered.Close())
}

func TestWatchRejectsMalformedOversizedAndOrdering(t *testing.T) {
	h := newTestHub(t, Options{})
	valid, _ := json.Marshal(h.Snapshot())
	for i, payload := range [][]byte{[]byte("{bad}\n"), bytes.Repeat([]byte("x"), MaxMessageBytes+1), []byte("{}\n"), append(append(append([]byte{}, valid...), '\n'), append(valid, '\n')...)} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			path := testSocket(t)
			listener, err := net.Listen("unix", path)
			require.NoError(t, err)
			require.NoError(t, os.Chmod(path, 0o600))
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err == nil {
					defer conn.Close()
					conn.SetWriteDeadline(time.Now().Add(time.Second))
					_, _ = conn.Write(payload)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			require.Error(t, Watch(ctx, path, func(Event) error { return nil }))
			<-done
		})
	}
}

func TestSocketPathAndServerContext(t *testing.T) {
	dir := filepath.Dir(testSocket(t))
	t.Setenv("XDG_RUNTIME_DIR", dir)
	path, err := SocketPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "ginkgo-voice.sock"), path)
	h := newTestHub(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	server, err := Listen(ctx, path, h)
	require.NoError(t, err)
	cancel()
	require.Eventually(t, func() bool { _, err := os.Lstat(path); return os.IsNotExist(err) }, time.Second, time.Millisecond)
	require.NoError(t, server.Close())
	_, err = Listen(ctx, path, h)
	require.ErrorIs(t, err, context.Canceled)
}

func TestServerClientLimitAndHubSurvivesShutdown(t *testing.T) {
	h := newTestHub(t, Options{MaxClients: 1})
	path, server := startTestServer(t, h)
	first, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer first.Close()
	var snapshot Event
	require.NoError(t, json.NewDecoder(first).Decode(&snapshot))
	second, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer second.Close()
	require.NoError(t, second.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = second.Read(make([]byte, 1))
	require.Error(t, err)
	var timeout net.Error
	require.False(t, errors.As(err, &timeout) && timeout.Timeout())
	require.NoError(t, server.Close())
	require.NoError(t, h.UpdateState(State{Listening: true}))
	require.True(t, h.Snapshot().Listening)
}

func TestWatchConsumerErrorAndUnsafeEndpoints(t *testing.T) {
	h := newTestHub(t, Options{})
	path, _ := startTestServer(t, h)
	consumerErr := errors.New("consumer stopped")
	require.ErrorIs(t, Watch(context.Background(), path, func(Event) error { return consumerErr }), consumerErr)
	require.NoError(t, os.Chmod(path, 0o666))
	require.ErrorContains(t, Watch(context.Background(), path, func(Event) error { return nil }), "unsafe endpoint")
	_, err := Listen(context.Background(), path, h)
	require.ErrorContains(t, err, "unsafe existing")
	require.NoError(t, os.Chmod(path, 0o600))
	link := filepath.Join(filepath.Dir(path), "link.sock")
	require.NoError(t, os.Symlink(path, link))
	require.ErrorContains(t, Watch(context.Background(), link, func(Event) error { return nil }), "unsafe endpoint")
	parentLink := filepath.Join(filepath.Dir(path), "directory")
	require.NoError(t, os.Symlink(filepath.Dir(path), parentLink))
	_, err = Listen(context.Background(), filepath.Join(parentLink, "new.sock"), h)
	require.ErrorContains(t, err, "symlink")
}
