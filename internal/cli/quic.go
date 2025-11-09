package cli

import (
	"context"
	"fmt"
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
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Start a minimal QUIC server",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if addr == "" {
				addr = ":7845"
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Starting QUIC server on %s\n", addr)
			return qnet.Serve(ctx, addr)
		},
	}
	serve.Flags().StringVar(&addr, "addr", ":7845", "listen address (host:port)")

	// quic ping <addr>
	ping := &cobra.Command{
		Use:   "ping <addr>",
		Short: "Ping a QUIC server and expect pong",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			rtt, err := qnet.Ping(ctx, args[0])
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "pong in %s\n", rtt)
			return nil
		},
	}

	cmd.AddCommand(serve, ping)
	return cmd
}
