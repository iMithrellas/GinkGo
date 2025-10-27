package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"google.golang.org/protobuf/proto"
)

// UnixListener listens on a Unix domain socket path.
type UnixListener struct{ Path string }

func (u UnixListener) Listen(ctx context.Context) (net.Listener, error) {
	// Remove stale socket
	_ = os.Remove(u.Path)
	l, err := net.Listen("unix", u.Path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(u.Path, 0o600)
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	return l, nil
}

// UnixServer implements Server for Unix sockets with length-prefixed protobuf.
type UnixServer struct{ L Listener }

func NewUnixServer(l Listener) *UnixServer { return &UnixServer{L: l} }

func (s *UnixServer) Serve(ctx context.Context, h Handler) error {
	l, err := s.L.Listen(ctx)
	if err != nil {
		return err
	}
	defer l.Close()
	errc := make(chan error, 1)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				errc <- err
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				// The handler expects pb.Request/Response but the interface is any.
				// Callers will wrap this Server with a typed adaptor.
				// Here we just operate on proto.Message.
				// Read generic request
				// The typed adaptor will provide concrete messages via Handler.
				reqMsg, respMsg, err := dispatchProto(conn, h)
				if err != nil {
					return
				}
				// write response
				_ = writeProto(conn, respMsg)
				_ = reqMsg // ensure req consumed
			}(c)
		}
	}()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		// If context canceled shortly after, suppress spurious errors
		if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
			return nil
		}
		return err
	}
}

// dispatchProto reads a proto message, hands it to Handler, returns response.
func dispatchProto(conn net.Conn, h Handler) (proto.Message, proto.Message, error) {
	// We don't know concrete types here; a typed adaptor should wrap Handler to
	// construct the right Request/Response types. To keep this generic, we rely
	// on the Handler to signal which types it expects via a small convention:
	// the Handler must accept and return proto.Message values.
	// We call Handle with a nil request to obtain a zero value, then fill it.
	// If the handler returns a non-nil proto.Message alongside a nil error as a template,
	// use it. Otherwise, we cannot proceed.
	tmplReq, _, err := prototype(h)
	if err != nil {
		return nil, nil, err
	}
	if err := readProto(conn, tmplReq); err != nil {
		return nil, nil, err
	}
	r, err := h.Handle(context.Background(), tmplReq)
	if err != nil {
		return tmplReq, nil, err
	}
	pm, _ := r.(proto.Message)
	return tmplReq, pm, nil
}

// prototype asks the handler for zero-value messages by passing a special nil.
// If the handler implements the optional ProtoTypes interface, use it.
type ProtoTypes interface {
	ProtoTypes() (req proto.Message, resp proto.Message)
}

func prototype(h Handler) (proto.Message, proto.Message, error) {
	if pt, ok := h.(ProtoTypes); ok {
		req, resp := pt.ProtoTypes()
		return req, resp, nil
	}
	// Fallback: time out quickly to avoid hanging if handler can't provide types
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
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := writeProto(conn, pmReq); err != nil {
		return nil, err
	}
	// The caller should provide a concrete response to unmarshal into via context?
	// For simplicity, we require the caller to pass a pointer to the response type in ctx
	// under key respTypeKey.
	rt := ctx.Value(respTypeKey{})
	pmResp, ok := rt.(proto.Message)
	if !ok || pmResp == nil {
		return nil, os.ErrInvalid
	}
	// Set deadline to respect context
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(dl)
	}
	if err := readProto(conn, pmResp); err != nil {
		return nil, err
	}
	return pmResp, nil
}

// respTypeKey is a context key for passing an empty response message instance.
type respTypeKey struct{}

// WithResp allocates a response container to unmarshal into.
func WithResp(ctx context.Context, resp proto.Message) context.Context {
	return context.WithValue(ctx, respTypeKey{}, resp)
}
