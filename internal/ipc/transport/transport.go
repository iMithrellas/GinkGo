package transport

import (
	"context"
	"net"
)

// Handler processes a single protobuf Request and returns a Response.
// Concrete types are generated in internal/ipc/pb.
// We depend on them indirectly to keep this package small and reusable.
type Handler interface {
	Handle(ctx context.Context, req any) (resp any, err error)
}

// Server is a generic server transport that accepts connections and
// dispatches length-prefixed protobuf messages to a Handler.
type Server interface {
	// Serve blocks, handling requests until ctx is done or an error occurs.
	Serve(ctx context.Context, h Handler) error
}

// Client is a generic client transport for request/response over a
// single logical connection per call.
type Client interface {
	// Do sends req and waits for resp using a single request/response round trip.
	Do(ctx context.Context, req any) (resp any, err error)
}

// Listener abstracts how a server obtains a net.Listener (unix, tcp, etc.).
// This allows reusing the same Server implementation with different endpoints.
type Listener interface {
	Listen(ctx context.Context) (net.Listener, error)
}
