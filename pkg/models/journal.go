package models

import (
	"context"

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/pkg/api"
)

// Journal is a higher-level façade for entries.
type Journal struct{ store *db.Store }

func NewJournal(store *db.Store) *Journal { return &Journal{store: store} }

func (j *Journal) Add(ctx context.Context, e api.Entry) error {
	_, err := j.store.Entries.CreateEntry(ctx, e)
	return err
}

func (j *Journal) Get(ctx context.Context, id string) (api.Entry, error) {
	return j.store.Entries.GetEntry(ctx, id)
}
