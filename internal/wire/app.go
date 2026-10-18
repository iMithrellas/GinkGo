package wire

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/imagecontext"
	synsvc "github.com/mithrel/ginkgo/internal/sync"
)

// App aggregates the major services for easy injection.
type App struct {
	Cfg    *viper.Viper
	Log    *log.Logger
	Store  *db.Store
	Syncer *synsvc.Service
	Images imagecontext.Store
}

// BuildApp wires dependencies with the provided config.
func BuildApp(ctx context.Context, cfg *viper.Viper) (*App, error) {
	logger := log.New(os.Stdout, "ginkgo ", log.LstdFlags)
	// Build DSN from DataDir: sqlite://<data_dir>/ginkgo.db
	dsn := "sqlite://" + filepath.Join(cfg.GetString("data_dir"), "ginkgo.db")
	store, err := db.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	syncer := synsvc.New(cfg, store)
	return &App{
		Cfg:    cfg,
		Log:    logger,
		Store:  store,
		Syncer: syncer,
		Images: imagecontext.NewStore(blobstore.NewFileStore(filepath.Join(cfg.GetString("data_dir"), "attachments"))),
	}, nil
}
