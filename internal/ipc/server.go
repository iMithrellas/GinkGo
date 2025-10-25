package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
)

// Serve starts a Unix domain socket server at path and handles one JSON
// Message per connection, replying with a JSON Response.
func Serve(ctx context.Context, path string, handle func(Message) Response) error {
	// Remove stale socket if present
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer l.Close()
	_ = os.Chmod(path, 0o600)

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
				dec := json.NewDecoder(bufio.NewReader(conn))
				var m Message
				if err := dec.Decode(&m); err != nil {
					_ = json.NewEncoder(conn).Encode(Response{OK: false, Msg: "bad request"})
					return
				}
				resp := handle(m)
				_ = json.NewEncoder(conn).Encode(resp)
			}(c)
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		log.Printf("ipc server error: %v", err)
		return err
	}
}
