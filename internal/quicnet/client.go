package quicnet

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"time"

	quic "github.com/quic-go/quic-go"
)

// Ping connects to addr, sends "ping" and waits for "pong". Returns RTT.
func Ping(ctx context.Context, addr string) (time.Duration, error) {
	// For MVP we skip verification; in production supply roots/pins.
	tlsConf := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{alpn}}
	start := time.Now()
	conn, err := quic.DialAddr(ctx, addr, tlsConf, &quic.Config{})
	if err != nil {
		return 0, err
	}
	defer conn.CloseWithError(0, "done")
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	if _, err := s.Write([]byte("ping")); err != nil {
		return 0, err
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(s, buf); err != nil {
		return 0, err
	}
	if string(buf) != "pong" {
		return 0, ErrBadPong
	}
	return time.Since(start), nil
}

var ErrBadPong = errors.New("unexpected response (not pong)")
