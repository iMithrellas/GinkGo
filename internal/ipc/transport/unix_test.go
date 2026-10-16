package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type testHandler struct {
	fn func(context.Context, any) (any, error)
}

func (h testHandler) Handle(ctx context.Context, req any) (any, error) { return h.fn(ctx, req) }
func (testHandler) ProtoTypes() (proto.Message, proto.Message) {
	return &wrapperspb.BytesValue{}, &wrapperspb.BytesValue{}
}

type suppliedListener struct{ net.Listener }

func (l suppliedListener) Listen(context.Context) (net.Listener, error) { return l.Listener, nil }

type observedListener struct {
	net.Listener
	accepted chan struct{}
}

func (l observedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted <- struct{}{}
	}
	return conn, err
}

func testSocket(t *testing.T) (net.Listener, string) {
	t.Helper()
	// Keep below Unix socket path limits even with long subtest names.
	dir, err := os.MkdirTemp("", "ipc-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not complete")
	}
}

func awaitError(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not complete")
		return nil
	}
}

func runTestServer(t *testing.T, ctx context.Context, l net.Listener, h Handler) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- NewUnixServer(suppliedListener{l}).Serve(ctx, h) }()
	return done
}

func TestRequestTimeoutPolicy(t *testing.T) {
	start := time.Now()
	ctx, cancel := requestContext(context.Background())
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok || dl.Sub(start) < 29*time.Second || dl.Sub(start) > 31*time.Second {
		t.Fatalf("ordinary deadline: %v", dl)
	}
	for _, duration := range []time.Duration{time.Second, 5 * time.Minute} {
		parent, stop := context.WithTimeout(context.Background(), duration)
		child, stopChild := requestContext(parent)
		want, _ := parent.Deadline()
		got, _ := child.Deadline()
		stopChild()
		stop()
		if got != want {
			t.Fatalf("explicit deadline replaced: got %v, want %v", got, want)
		}
	}
}

func TestUnixClientCancellationInterruptsIO(t *testing.T) {
	for _, writing := range []bool{false, true} {
		name := "read"
		if writing {
			name = "write"
		}
		t.Run(name, func(t *testing.T) {
			l, path := testSocket(t)
			blocked := make(chan struct{})
			writeFinished, releaseWrite := context.WithCancel(context.Background())
			defer releaseWrite()
			peerDone := make(chan error, 1)
			go func() {
				conn, err := l.Accept()
				if err != nil {
					peerDone <- err
					return
				}
				defer conn.Close()
				if writing {
					var first [1]byte
					_, err = io.ReadFull(conn, first[:])
				} else {
					err = readProto(conn, &wrapperspb.BytesValue{})
				}
				close(blocked)
				if err != nil {
					peerDone <- err
					return
				}
				if writing {
					// Do not drain until cancellation has interrupted the blocked write.
					<-writeFinished.Done()
				}
				_, err = io.Copy(io.Discard, conn)
				peerDone <- err
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := wrapperspb.Bytes([]byte("request"))
			if writing {
				request = wrapperspb.Bytes(make([]byte, 8<<20))
			}
			done := make(chan error, 1)
			go func() {
				_, err := NewUnixClient(path).Do(WithResp(ctx, &wrapperspb.BytesValue{}), request)
				done <- err
			}()
			awaitSignal(t, blocked)
			cancel()
			if err := awaitError(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
			releaseWrite()
			if err := awaitError(t, peerDone); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnixClientDeadline(t *testing.T) {
	l, path := testSocket(t)
	peerDone := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer conn.Close()
		_, err = io.Copy(io.Discard, conn)
		peerDone <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := NewUnixClient(path).Do(WithResp(ctx, &wrapperspb.BytesValue{}), wrapperspb.Bytes(nil))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if err := awaitError(t, peerDone); err != nil {
		t.Fatal(err)
	}
}

func TestUnixServerDisconnectAndShutdownCancelHandler(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "disconnect"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			l, path := testSocket(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			stopped := make(chan struct{})
			done := runTestServer(t, ctx, l, testHandler{fn: func(ctx context.Context, _ any) (any, error) {
				close(started)
				<-ctx.Done()
				close(stopped)
				return nil, ctx.Err()
			}})
			conn, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := writeProto(conn, wrapperspb.Bytes([]byte("request"))); err != nil {
				t.Fatal(err)
			}
			awaitSignal(t, started)
			if shutdown {
				cancel()
			} else {
				_ = conn.Close()
			}
			awaitSignal(t, stopped)
			cancel()
			if err := awaitError(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnixServerShutdownInterruptsInitialRead(t *testing.T) {
	l, path := testSocket(t)
	accepted := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runTestServer(t, ctx, observedListener{Listener: l, accepted: accepted}, testHandler{fn: func(context.Context, any) (any, error) {
		t.Error("partial request reached handler")
		return nil, nil
	}})
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	awaitSignal(t, accepted)
	// A payload length without the payload leaves readProto blocked.
	if _, err := conn.Write([]byte{100}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := awaitError(t, done); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained open")
	}
}

func TestUnixServerShutdownInterruptsResponseWrite(t *testing.T) {
	l, path := testSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runTestServer(t, ctx, l, testHandler{fn: func(context.Context, any) (any, error) {
		return wrapperspb.Bytes(make([]byte, 8<<20)), nil
	}})
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeProto(conn, wrapperspb.Bytes(nil)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var first [1]byte
	if _, err := io.ReadFull(conn, first[:]); err != nil {
		t.Fatal(err)
	}
	// The response is much larger than a Unix socket buffer. Leave it undrained
	// and require shutdown to close the connection and join the writer worker.
	cancel()
	if err := awaitError(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestUnixListenerCancellationAndEarlyClose(t *testing.T) {
	for _, earlyClose := range []bool{false, true} {
		name := "context cancellation"
		if earlyClose {
			name = "early close"
		}
		t.Run(name, func(t *testing.T) {
			initial, path := testSocket(t)
			_ = initial.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			l, err := (UnixListener{Path: path}).Listen(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := make(chan error, 1)
			go func() { _, err := l.Accept(); done <- err }()
			if earlyClose {
				_ = l.Close()
			} else {
				cancel()
			}
			if err := awaitError(t, done); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestUnixOrdinaryRequestsPropagateContext(t *testing.T) {
	type key struct{}
	l, path := testSocket(t)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "daemon"))
	defer cancel()
	done := runTestServer(t, ctx, l, testHandler{fn: func(ctx context.Context, req any) (any, error) {
		if ctx.Value(key{}) != "daemon" {
			t.Error("daemon context lost")
		}
		return req, nil
	}})
	for range 20 {
		got, err := NewUnixClient(path).Do(WithResp(context.Background(), &wrapperspb.BytesValue{}), wrapperspb.Bytes([]byte("ordinary")))
		if err != nil || string(got.(*wrapperspb.BytesValue).Value) != "ordinary" {
			t.Fatalf("got %v, %v", got, err)
		}
	}
	cancel()
	if err := awaitError(t, done); err != nil {
		t.Fatal(err)
	}
}
