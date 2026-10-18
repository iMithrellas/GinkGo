package blobstore

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImmutableScopedStore(t *testing.T) {
	ctx := context.Background()
	s := NewFileStore(t.TempDir())
	const id = "attachment-0123456789"
	require.NoError(t, s.Put(ctx, "work", id, []byte("original")))
	require.NoError(t, s.Put(ctx, "work", id, []byte("original")))
	require.ErrorIs(t, s.Put(ctx, "work", id, []byte("changed")), ErrConflict)
	_, err := s.Get(ctx, "personal", id)
	require.ErrorIs(t, err, os.ErrNotExist)
	data, err := s.Get(ctx, "work", id)
	require.NoError(t, err)
	require.Equal(t, "original", string(data))
	require.Error(t, s.Put(ctx, "work", "../../escape", []byte("data")))
	path, err := s.path("work", id)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, s.Put(canceled, "work", "attachment-canceled", []byte("data")), context.Canceled)
	_, err = s.Get(ctx, "work", "attachment-canceled")
	require.ErrorIs(t, err, os.ErrNotExist)
}
