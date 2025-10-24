package wire

import (
    "context"
    "log"
    "os"

    "github.com/mithrel/ginkgo/internal/config"
    "github.com/mithrel/ginkgo/internal/db"
    synsvc "github.com/mithrel/ginkgo/internal/sync"
)

// App aggregates the major services for easy injection.
type App struct {
    Cfg    config.Config
    Log    *log.Logger
    Store  db.Store
    Syncer *synsvc.Service
}

// BuildApp wires dependencies with the provided config.
func BuildApp(ctx context.Context, cfg config.Config) (*App, error) {
    logger := log.New(os.Stdout, "ginkgo ", log.LstdFlags)
    store, err := db.Open(ctx, cfg.DBURL)
    if err != nil { return nil, err }
    syncer := synsvc.New()
    return &App{
        Cfg: cfg,
        Log: logger,
        Store: store,
        Syncer: syncer,
    }, nil
}

