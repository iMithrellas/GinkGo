package cli

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/server"
	"github.com/mithrel/ginkgo/internal/wire"
)

func newServerCmd() *cobra.Command {
	var cfgPath string
	var listen string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the HTTP replication server",
		RunE: func(cmd *cobra.Command, args []string) error {
			v := viper.New()
			if cfgPath != "" {
				v.SetConfigFile(cfgPath)
			}
			if err := config.Load(cmd.Context(), v); err != nil {
				return err
			}
			app, err := wire.BuildApp(cmd.Context(), v)
			if err != nil {
				return err
			}
			if listen != "" {
				v.Set("http_addr", listen)
			}
			if strings.TrimSpace(v.GetString("auth.token")) == "" {
				return fmt.Errorf("auth.token is required for the replication server")
			}
			addr := v.GetString("http_addr")
			if addr == "" {
				addr = ":8080"
			}
			srv := server.New(v, app.Store)
			httpSrv := &http.Server{Addr: addr, Handler: srv.Router()}
			fmt.Fprintf(cmd.OutOrStdout(), "HTTP replication server listening on %s\n", addr)
			return httpSrv.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (yaml|toml)")
	cmd.Flags().StringVar(&listen, "listen", "", "listen address (override config http_addr)")
	return cmd
}
