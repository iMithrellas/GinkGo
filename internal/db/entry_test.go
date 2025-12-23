package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) (*Store, context.Context, context.CancelFunc) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	ctx, cancel := context.WithCancel(context.Background())

	store, closer, err := openSQLite(ctx, "sqlite://"+dbPath)
	require.NoError(t, err)

	t.Cleanup(func() {
		closer.Close()
		os.RemoveAll(tmpDir)
	})

	return &Store{Events: store.Events, Entries: store.Entries}, ctx, cancel
}

func TestUpdateEntryCAS(t *testing.T) {
	store, ctx, _ := setupTestDB(t)
	repo := store.Entries

	now := time.Now().UTC().Truncate(time.Second)
	initial := api.Entry{
		ID:        "note-1",
		Version:   1,
		Title:     "Initial",
		Body:      "Body",
		Tags:      []string{"tag1"},
		Namespace: "test",
		CreatedAt: now,
		UpdatedAt: now,
	}

	t.Run("CreateEntry initializes version", func(t *testing.T) {
		created, err := repo.CreateEntry(ctx, initial)
		require.NoError(t, err)
		assert.Equal(t, int64(1), created.Version)
	})

	t.Run("UpdateEntryCAS increments version normally", func(t *testing.T) {
		cur, err := repo.GetEntry(ctx, initial.ID)
		require.NoError(t, err)

		// Simulate local edit incrementing version
		cur.Title = "Updated Locally"
		cur.Version = cur.Version + 1

		updated, err := repo.UpdateEntryCAS(ctx, cur, 1) // ifVersion is 1
		require.NoError(t, err)
		assert.Equal(t, int64(2), updated.Version)
		assert.Equal(t, "Updated Locally", updated.Title)
	})

	t.Run("UpdateEntryCAS allows explicit version jump (Sync)", func(t *testing.T) {
		cur, err := repo.GetEntry(ctx, initial.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(2), cur.Version)

		// Simulate sync pulling Version 10
		cur.Title = "Sync Update"
		cur.Version = 10

		updated, err := repo.UpdateEntryCAS(ctx, cur, 2) // ifVersion is 2
		require.NoError(t, err)
		assert.Equal(t, int64(10), updated.Version)
		assert.Equal(t, "Sync Update", updated.Title)
	})

	t.Run("UpdateEntryCAS fails on version mismatch (Conflict)", func(t *testing.T) {
		cur, err := repo.GetEntry(ctx, initial.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(10), cur.Version)

		cur.Title = "Conflicting Update"
		cur.Version = 11

		_, err = repo.UpdateEntryCAS(ctx, cur, 9) // wrong ifVersion
		assert.ErrorIs(t, err, ErrConflict)

		// Verify DB state hasn't changed
		final, err := repo.GetEntry(ctx, initial.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(10), final.Version)
		assert.Equal(t, "Sync Update", final.Title)
	})

	t.Run("UpdateEntryCAS allows setting same version (Idempotent update)", func(t *testing.T) {
		cur, err := repo.GetEntry(ctx, initial.ID)
		require.NoError(t, err)

		// If content changed but we want to keep version (unlikely but possible via replication)
		cur.Body = "New Body"
		// cur.Version is 10

		updated, err := repo.UpdateEntryCAS(ctx, cur, 10)
		require.NoError(t, err)
		assert.Equal(t, int64(10), updated.Version)
		assert.Equal(t, "New Body", updated.Body)
	})
}
