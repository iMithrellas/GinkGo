package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/internal/ipc/transport"
)

type lifecycleListener struct{ net.Listener }

func (l lifecycleListener) Listen(context.Context) (net.Listener, error) { return l.Listener, nil }

func lifecycleResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("daemon lifecycle did not complete")
		return nil
	}
}

func lifecycleSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler lifecycle did not advance")
	}
}

func TestDaemonShutdownJoinsIPCHandlerCleanup(t *testing.T) {
	for _, stopHTTP := range []bool{false, true} {
		name := "context cancellation"
		if stopHTTP {
			name = "HTTP stops"
		}
		t.Run(name, func(t *testing.T) {
			// Keep Unix paths below the kernel limit regardless of test name.
			dir, err := os.MkdirTemp("", "daemon-lifecycle-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "s")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cleanupGate, releaseCleanup := context.WithCancel(context.Background())
			defer releaseCleanup()
			started := make(chan struct{})
			cleaning := make(chan struct{})
			cleaned := make(chan struct{})
			handler := ipc.PBHandlerContext(func(ctx context.Context, _ ipc.Message) ipc.Response {
				close(started)
				<-ctx.Done()
				close(cleaning)
				// Simulate a cooperative provider reaping its canceled child.
				<-cleanupGate.Done()
				close(cleaned)
				return ipc.Response{OK: false}
			})
			httpServer := &http.Server{Addr: "127.0.0.1:0"}
			done := make(chan error, 1)
			go func() {
				done <- serveDaemon(ctx, httpServer, transport.NewUnixServer(lifecycleListener{listener}), handler)
			}()
			clientDone := make(chan error, 1)
			go func() {
				_, err := transport.NewUnixClient(path).Do(transport.WithResp(ctx, &pb.Response{}), &pb.Request{})
				clientDone <- err
			}()
			lifecycleSignal(t, started)
			if stopHTTP {
				_ = httpServer.Close()
			} else {
				cancel()
			}
			lifecycleSignal(t, cleaning)
			select {
			case err := <-done:
				t.Fatalf("daemon returned before handler cleanup: %v", err)
			case <-time.After(150 * time.Millisecond):
				// Longer than the old fixed 100 ms sleep, still gated explicitly.
			}
			releaseCleanup()
			if err := lifecycleResult(t, done); !errors.Is(err, http.ErrServerClosed) {
				t.Fatalf("unexpected HTTP shutdown result: %v", err)
			}
			lifecycleSignal(t, cleaned)
			if err := lifecycleResult(t, clientDone); err == nil {
				t.Fatal("canceled request succeeded")
			}
		})
	}
}

type failingIPCServer struct{ err error }

func (s failingIPCServer) Serve(context.Context, transport.Handler) error { return s.err }

func TestDaemonIPCFailureStopsHTTP(t *testing.T) {
	want := errors.New("IPC serve failed")
	for _, serveErr := range []error{want, nil} {
		name := "unexpected stop"
		if serveErr != nil {
			name = "serve failure"
		}
		t.Run(name, func(t *testing.T) {
			httpServer := &http.Server{Addr: "127.0.0.1:0"}
			done := make(chan error, 1)
			go func() { done <- serveDaemon(context.Background(), httpServer, failingIPCServer{serveErr}, nil) }()
			err := lifecycleResult(t, done)
			if err == nil || (serveErr != nil && !errors.Is(err, want)) {
				t.Fatalf("lost IPC error: %v", err)
			}
			// Shutdown permanently marks Server as stopped, even if IPC failed
			// before the HTTP serving goroutine started listening.
			if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				t.Fatalf("HTTP server still usable after IPC stopped: %v", err)
			}
		})
	}
}

func TestDaemonIPCStartupFailureStopsHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-directory", "socket")
	httpServer := &http.Server{Addr: "127.0.0.1:0"}
	done := make(chan error, 1)
	go func() {
		done <- serveDaemon(context.Background(), httpServer, transport.NewUnixServer(transport.UnixListener{Path: path}), nil)
	}()
	if err := lifecycleResult(t, done); err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("lost socket startup error: %v", err)
	}
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("HTTP did not stop: %v", err)
	}
}
