package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/cli"
	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/daemon"
	"github.com/mithrel/ginkgo/internal/wire"
)

func main() {
	if invokedAsDaemon() {
		if err := runDaemon(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func invokedAsDaemon() bool {
	return filepath.Base(os.Args[0]) == "ginkgod"
}

func runDaemon() error {
	fs := flag.NewFlagSet("ginkgod", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cfgPath string
	fs.StringVar(&cfgPath, "config", "", "path to config file (yaml|toml)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	v := viper.New()
	if cfgPath != "" {
		v.SetConfigFile(cfgPath)
	}
	if err := config.Load(ctx, v); err != nil {
		return err
	}
	app, err := wire.BuildApp(ctx, v)
	if err != nil {
		return err
	}
	return daemon.Run(ctx, app)
}
