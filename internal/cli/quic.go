package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	qnet "github.com/mithrel/ginkgo/internal/quicnet"
)

func newQuicCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quic",
		Short: "Experimental QUIC tools (server and ping)",
	}

	// quic serve --addr :7845
	var addr string
	var domain string
	var email string
	var httpAddr string
	var storageDir string
	var acmeCA string
	var eabKeyID string
	var eabHMAC string
	var zerosslAPI string
	var enableTLSALPN bool
	var alpnPort int
	var certFile string
	var keyFile string
	var insecureSelfSigned bool
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Start a minimal QUIC server",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if addr == "" {
				addr = ":7845"
			}
			var tlsConf *tls.Config
			var httpSrv *http.Server
			// Prioritize BYO cert
			if certFile != "" || keyFile != "" {
				if certFile == "" || keyFile == "" {
					return fmt.Errorf("both --cert and --key are required")
				}
				c, err := qnet.BuildFileTLS(certFile, keyFile)
				if err != nil {
					return err
				}
				tlsConf = c
			} else if domain != "" {
				if httpAddr == "" {
					httpAddr = ":80"
				}
				c, h, err := qnet.BuildCertMagicTLS(qnet.CertMagicConfig{
					Domain:         domain,
					Email:          email,
					StorageDir:     storageDir,
					CA:             acmeCA,
					EABKeyID:       eabKeyID,
					EABHMACKey:     eabHMAC,
					ZeroSSLAPIKey:  zerosslAPI,
					EnableHTTP01:   httpAddr != "",
					HTTPAddr:       httpAddr,
					EnableTLSALPN:  enableTLSALPN,
					AltTLSALPNPort: alpnPort,
				})
				if err != nil {
					return err
				}
				tlsConf = c
				if h != nil {
					httpSrv = &http.Server{Addr: httpAddr, Handler: h}
					go func() { _ = httpSrv.ListenAndServe() }()
				}
			} else if insecureSelfSigned {
				c, err := qnet.SelfSignedTLS()
				if err != nil {
					return err
				}
				tlsConf = c
			} else {
				return fmt.Errorf("TLS required: provide --cert/--key or --domain; for testing use --insecure-self-signed")
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Starting QUIC server on %s\n", addr)
			err := qnet.Serve(ctx, addr, tlsConf)
			if httpSrv != nil {
				_ = httpSrv.Close()
			}
			return err
		},
	}
	serve.Flags().StringVar(&addr, "addr", ":7845", "listen address (host:port)")
	serve.Flags().StringVar(&domain, "domain", "", "domain for ACME (CertMagic)")
	serve.Flags().StringVar(&email, "email", "", "email for ACME registration")
	serve.Flags().StringVar(&httpAddr, "http-addr", ":80", "HTTP address for ACME HTTP-01 challenge")
	serve.Flags().StringVar(&storageDir, "storage-dir", "", "CertMagic storage directory (optional)")
	serve.Flags().StringVar(&acmeCA, "acme-ca", "", "ACME directory URL (optional; defaults to Let's Encrypt prod)")
	serve.Flags().StringVar(&eabKeyID, "eab-key-id", "", "ACME External Account Binding key ID (optional)")
	serve.Flags().StringVar(&eabHMAC, "eab-hmac", "", "ACME External Account Binding HMAC key (optional)")
	serve.Flags().StringVar(&zerosslAPI, "zerossl-api-key", "", "Use ZeroSSL API issuer (bypass ACME) with this API key")
	serve.Flags().StringVar(&certFile, "cert", "", "path to TLS certificate (PEM)")
	serve.Flags().StringVar(&keyFile, "key", "", "path to TLS private key (PEM)")
	serve.Flags().BoolVar(&insecureSelfSigned, "insecure-self-signed", false, "use an ephemeral self-signed cert (NOT recommended)")
	serve.Flags().BoolVar(&enableTLSALPN, "enable-tls-alpn", false, "enable ACME TLS-ALPN-01 challenge (requires TCP :443 or --alpn-port)")
	serve.Flags().IntVar(&alpnPort, "alpn-port", 443, "alternate port for TLS-ALPN-01 challenge")

	// quic ping <addr>
	var pingServerName string
	var pingInsecure bool
	ping := &cobra.Command{
		Use:   "ping <addr>",
		Short: "Ping a QUIC server and expect pong",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			rtt, err := qnet.Ping(ctx, args[0], pingServerName, pingInsecure)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "pong in %s\n", rtt)
			return nil
		},
	}
	ping.Flags().StringVar(&pingServerName, "server-name", "", "expected server name (SNI) for TLS verification; leave empty to skip verification")
	ping.Flags().BoolVar(&pingInsecure, "insecure", false, "skip TLS verification for ping")

	cmd.AddCommand(serve, ping)
	return cmd
}
