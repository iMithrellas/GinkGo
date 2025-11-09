package quicnet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
)

// CertMagicConfig configures automatic certificate management with CertMagic.
type CertMagicConfig struct {
	Domain     string
	Email      string
	StorageDir string // optional; defaults to XDG or ~/.cache/ginkgo/certmagic
	CA         string // optional; defaults to Let's Encrypt prod
	// Optional EAB for providers that require it (e.g., ZeroSSL)
	EABKeyID      string
	EABHMACKey    string
	ZeroSSLAPIKey string
	// Challenges
	EnableHTTP01   bool   // if true, return HTTP handler for :80
	HTTPAddr       string // ":80" by default, used only by caller when serving handler
	EnableTLSALPN  bool   // if true, allow TLS-ALPN-01; requires TCP :443 or AltTLSALPNPort
	AltTLSALPNPort int    // optional alternate port for TLS-ALPN challenge
}

// BuildCertMagicTLS provisions/loads certificates via CertMagic and returns a
// TLS config for QUIC plus an HTTP handler for HTTP-01 challenges.
func BuildCertMagicTLS(cfg CertMagicConfig) (*tls.Config, http.Handler, error) {
	if cfg.Domain == "" {
		return nil, nil, errors.New("domain is required")
	}

	cm := certmagic.NewDefault()
	// Storage
	if cfg.StorageDir == "" {
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			cfg.StorageDir = filepath.Join(xdg, "ginkgo", "certmagic")
		} else {
			home, _ := os.UserHomeDir()
			cfg.StorageDir = filepath.Join(home, ".cache", "ginkgo", "certmagic")
		}
	}
	if err := os.MkdirAll(cfg.StorageDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("cert storage: %w", err)
	}
	cm.Storage = &certmagic.FileStorage{Path: cfg.StorageDir}

	// Issuer: ZeroSSL API or ACME
	var acmeIssuer *certmagic.ACMEIssuer
	if cfg.ZeroSSLAPIKey != "" {
		cm.Issuers = []certmagic.Issuer{&certmagic.ZeroSSLIssuer{APIKey: cfg.ZeroSSLAPIKey}}
	} else {
		disableHTTP := !cfg.EnableHTTP01
		disableALPN := !cfg.EnableTLSALPN
		ai := certmagic.NewACMEIssuer(cm, certmagic.ACMEIssuer{
			CA:                      ifEmpty(cfg.CA, certmagic.LetsEncryptProductionCA),
			Email:                   cfg.Email,
			Agreed:                  true,
			DisableHTTPChallenge:    disableHTTP,
			DisableTLSALPNChallenge: disableALPN,
			AltTLSALPNPort:          cfg.AltTLSALPNPort,
		})
		// Note: EAB can be wired via acmez if needed; omitted here for portability.
		acmeIssuer = ai
		cm.Issuers = []certmagic.Issuer{ai}
	}

	if err := cm.ManageSync(context.Background(), []string{cfg.Domain}); err != nil {
		return nil, nil, err
	}

	tlsConf := cm.TLSConfig()
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
	tlsConf.MinVersion = tls.VersionTLS13

	// Provide HTTP-01 handler only if enabled and ACME issuer in use
	if acmeIssuer != nil && cfg.EnableHTTP01 {
		h := acmeIssuer.HTTPChallengeHandler(nil)
		return tlsConf, h, nil
	}
	return tlsConf, nil, nil
}

func ifEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ParsePort extracts an integer port from a host:port address; returns 0 if absent.
func ParsePort(addr string) int {
	if addr == "" {
		return 0
	}
	lastColon := strings.LastIndex(addr, ":")
	if lastColon < 0 || lastColon == len(addr)-1 {
		return 0
	}
	p, _ := strconv.Atoi(addr[lastColon+1:])
	return p
}

// BuildFileTLS loads a certificate from PEM files for BYO certs.
func BuildFileTLS(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("both certFile and keyFile are required")
	}

	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load keypair: %w", err)
	}

	// Validate certificate chain
	for i, b := range c.Certificate {
		cert, err := x509.ParseCertificate(b)
		if err != nil {
			return nil, fmt.Errorf("invalid certificate at index %d: %w", i, err)
		}
		// Check if certificate is expired
		now := time.Now()
		if now.Before(cert.NotBefore) {
			return nil, fmt.Errorf("certificate not yet valid (starts %s)", cert.NotBefore)
		}
		if now.After(cert.NotAfter) {
			return nil, fmt.Errorf("certificate expired on %s", cert.NotAfter)
		}
	}

	return &tls.Config{Certificates: []tls.Certificate{c}, NextProtos: []string{alpn}, MinVersion: tls.VersionTLS13}, nil
}
