// Package transport provides generic protobuf IPC primitives.
// Types use 'any' to avoid direct dependencies on generated code in internal/ipc/pb.
package transport

import (
	"context"
	"net"
)

type Handler interface {
	Handle(ctx context.Context, req any) (resp any, err error)
}

// Server dispatches length-prefixed protobuf messages.
type Server interface {
	Serve(ctx context.Context, h Handler) error // blocks until done
}

type Client interface {
	Do(ctx context.Context, req any) (resp any, err error)
}

type Listener interface {
	Listen(ctx context.Context) (net.Listener, error)
}
