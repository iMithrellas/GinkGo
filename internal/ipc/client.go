package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
)

// Request sends a Message to the daemon and waits for a Response.
func Request(ctx context.Context, path string, m Message) (Response, error) {
	var r Response
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return r, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(m); err != nil {
		return r, err
	}
	dec := json.NewDecoder(bufio.NewReader(conn))
	if err := dec.Decode(&r); err != nil {
		return r, err
	}
	return r, nil
}
