package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

// UnixListener listens on a Unix domain socket path.
type UnixListener struct{ Path string }

func (u UnixListener) Listen(ctx context.Context) (net.Listener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Remove stale socket
	_ = os.Remove(u.Path)
	l, err := net.Listen("unix", u.Path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(u.Path, 0o600)
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	return &contextListener{Listener: l, stop: stop}, nil
}

// Stop watching a context when its listener is closed before cancellation.
type contextListener struct {
	net.Listener
	stop func() bool
}

func (l *contextListener) Close() error {
	l.stop()
	return l.Listener.Close()
}

// UnixServer implements Server for Unix sockets with length-prefixed protobuf.
type UnixServer struct{ L Listener }

func NewUnixServer(l Listener) *UnixServer { return &UnixServer{L: l} }

func (s *UnixServer) Serve(ctx context.Context, h Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	l, err := s.L.Listen(ctx)
	if err != nil {
		return err
	}
	var workers sync.WaitGroup
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer func() {
		cancel()
		stop()
		_ = l.Close()
		workers.Wait()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			serveConn(ctx, conn, h)
		}()
	}
}

// serveConn owns one request, its disconnect watcher and its connection. Handlers
// must honor context cancellation; shutdown waits for them to release resources.
func serveConn(parent context.Context, conn net.Conn, h Handler) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() {
		cancel()
		stop()
		_ = conn.Close()
	}()
	// We don't know concrete types here. Callers provide a typed adaptor that
	// implements ProtoTypes to supply zero values for the request/response
	// protobuf messages (e.g., &pb.Request{}, &pb.Response{}).
	tmplReq, _, err := prototype(h)
	if err != nil {
		return
	}
	if err := readProto(conn, tmplReq); err != nil {
		return
	}
	// The protocol permits only one request per connection. After its complete
	// frame, EOF (or unexpected additional input) cancels ongoing work. A watcher
	// is needed because handlers can run without doing any further socket I/O.
	disconnected := make(chan struct{})
	go func() {
		defer close(disconnected)
		var extra [1]byte
		_, _ = conn.Read(extra[:])
		cancel()
	}()
	defer func() {
		_ = conn.Close()
		<-disconnected
	}()
	r, err := h.Handle(ctx, tmplReq)
	if err != nil || ctx.Err() != nil {
		return
	}
	pm, ok := r.(proto.Message)
	if !ok {
		return
	}
	_ = writeProto(conn, pm)
}

// prototype asks the handler for zero-value request/response messages via the
// optional ProtoTypes interface. Handlers that don't implement ProtoTypes
// cannot be used with this generic transport.
type ProtoTypes interface {
	ProtoTypes() (req proto.Message, resp proto.Message)
}

func prototype(h Handler) (proto.Message, proto.Message, error) {
	if pt, ok := h.(ProtoTypes); ok {
		req, resp := pt.ProtoTypes()
		return req, resp, nil
	}
	// No ProtoTypes available: signal invalid usage of the transport.
	return nil, nil, os.ErrInvalid
}

// UnixClient implements Client for Unix sockets with length-prefixed protobuf.
type UnixClient struct{ Path string }

func NewUnixClient(path string) *UnixClient { return &UnixClient{Path: path} }

func (c *UnixClient) Do(ctx context.Context, req any) (any, error) {
	pmReq, ok := req.(proto.Message)
	if !ok {
		return nil, os.ErrInvalid
	}
	// An explicit deadline (including a long operation deadline) wins. Ordinary
	// requests retain the existing 30-second bound, now covering writes as well.
	ctx, cancel := requestContext(ctx)
	defer cancel()
	pmResp, ok := ctx.Value(respTypeKey{}).(proto.Message)
	if !ok || pmResp == nil {
		return nil, os.ErrInvalid
	}
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return nil, contextIOError(ctx, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return nil, contextIOError(ctx, err)
		}
	}
	if err := writeProto(conn, pmReq); err != nil {
		return nil, contextIOError(ctx, err)
	}
	// The caller must provide a zero response instance to unmarshal into via
	// context using WithResp(ctx, &pb.Response{}).
	if err := readProto(conn, pmResp); err != nil {
		return nil, contextIOError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return pmResp, nil
}

func contextIOError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Socket deadlines and the context timer may fire in either order.
	if dl, ok := ctx.Deadline(); ok && !time.Now().Before(dl) {
		return context.DeadlineExceeded
	}
	return err
}

func requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 30*time.Second)
}

// respTypeKey is a context key for passing an empty response message instance.
type respTypeKey struct{}

// WithResp allocates a response container to unmarshal into.
func WithResp(ctx context.Context, resp proto.Message) context.Context {
	return context.WithValue(ctx, respTypeKey{}, resp)
}

// Example typed adaptor (sketch):
//
//  type pbHandler struct{}
//  func (pbHandler) ProtoTypes() (proto.Message, proto.Message) {
//      return &pb.Request{}, &pb.Response{}
//  }
//  func (pbHandler) Handle(ctx context.Context, req any) (any, error) {
//      r := req.(*pb.Request)
//      // ... build response ...
//      return &pb.Response{Ok: true}, nil
//  }
