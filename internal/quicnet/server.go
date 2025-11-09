package quicnet

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"time"

	quic "github.com/quic-go/quic-go"
)

const alpn = "ginkgo-quic/1"

// Serve starts a minimal QUIC server with the provided TLS config. It replies
// "pong" to a single-line message equal to "ping" on the first stream of each
// connection.
func Serve(ctx context.Context, addr string, tlsConf *tls.Config) error {
	if tlsConf == nil {
		return ErrMissingTLS
	}
	// Ensure ALPN includes our protocol
	has := false
	for _, p := range tlsConf.NextProtos {
		if p == alpn {
			has = true
			break
		}
	}
	if !has {
		tlsConf.NextProtos = append(tlsConf.NextProtos, alpn)
	}

	l, err := quic.ListenAddr(addr, tlsConf, &quic.Config{})
	if err != nil {
		return err
	}
	defer l.Close()

	errc := make(chan error, 1)
	go func() {
		for {
			conn, err := l.Accept(ctx)
			if err != nil {
				errc <- err
				return
			}
			go handleConn(ctx, conn)
		}
	}()

	select {
	case <-ctx.Done():
		_ = l.Close()
		return nil
	case err := <-errc:
		return err
	}
}

func handleConn(ctx context.Context, conn quic.Connection) {

	s, err := conn.AcceptStream(ctx)
	if err != nil {
		return
	}
	defer s.Close()

	buf := make([]byte, 4)
	n, err := io.ReadFull(s, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return
	}
	if string(buf[:n]) == "ping" {
		_, _ = s.Write([]byte("pong"))
	}
}

// SelfSignedTLS is for testing only. Prefer trusted certs in production.
func SelfSignedTLS() (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	templ := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, templ, templ, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	tlsCert, err := tls.X509KeyPair(cert, priv)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{tlsCert}}, nil
}

var ErrMissingTLS = errors.New("missing TLS configuration")
