package sync_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/server"
	"github.com/mithrel/ginkgo/internal/sync"
	"github.com/mithrel/ginkgo/pkg/api"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func setupDB(t *testing.T, name string) *db.Store {
	dir := t.TempDir()
	dsn := "sqlite://" + filepath.Join(dir, name+".db")
	s, err := db.Open(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func setupSyncService(t *testing.T, s *db.Store, remoteURL, token string, dataDir string) *sync.Service {
	v := viper.New()
	v.Set("data_dir", dataDir)
	v.Set("remotes.origin.url", remoteURL)
	v.Set("remotes.origin.enabled", true)
	v.Set("remotes.origin.token", token)
	v.Set("sync.batch_size", 10)
	return sync.New(v, s)
}

func TestSyncBidirectional(t *testing.T) {
	ctx := context.Background()
	token := "test-token"

	// Server
	serverStore := setupDB(t, "server")
	srvCfg := viper.New()
	srvCfg.Set("auth.token", token)
	srv := server.New(srvCfg, serverStore)
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// Clients
	client1Store := setupDB(t, "client1")
	client1Sync := setupSyncService(t, client1Store, ts.URL, token, t.TempDir())

	client2Store := setupDB(t, "client2")
	client2Sync := setupSyncService(t, client2Store, ts.URL, token, t.TempDir())

	// 1. Client 1 creates Note 1
	note1 := api.Entry{ID: "note1", Title: "Note 1", Body: "Body 1", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_, err := client1Store.Entries.CreateEntry(ctx, note1)
	require.NoError(t, err)

	// 2. Client 2 creates Note 2
	note2 := api.Entry{ID: "note2", Title: "Note 2", Body: "Body 2", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_, err = client2Store.Entries.CreateEntry(ctx, note2)
	require.NoError(t, err)

	// 3. Client 1 Syncs (Push Note 1)
	err = client1Sync.SyncNow(ctx)
	require.NoError(t, err)

	evs1, _, err := serverStore.Events.List(ctx, api.Cursor{}, 100)
	require.NoError(t, err)
	require.NotEmpty(t, evs1)

	// 4. Client 2 Syncs (Push Note 2, Pull Note 1)
	err = client2Sync.SyncNow(ctx)
	require.NoError(t, err)

	evs2, _, err := serverStore.Events.List(ctx, api.Cursor{}, 100)
	require.NoError(t, err)
	require.Len(t, evs2, 2)

	c2n1, err := client2Store.Entries.GetEntry(ctx, "note1")
	require.NoError(t, err)
	require.Equal(t, "Note 1", c2n1.Title)

	// 5. Client 1 Syncs (Pull Note 2)
	err = client1Sync.SyncNow(ctx)
	require.NoError(t, err)

	c1n2, err := client1Store.Entries.GetEntry(ctx, "note2")
	require.NoError(t, err)
	require.Equal(t, "Note 2", c1n2.Title)
}

func TestSyncClockSkew(t *testing.T) {
	ctx := context.Background()
	token := "test-token"

	// Server
	serverStore := setupDB(t, "server_skew")
	srvCfg := viper.New()
	srvCfg.Set("auth.token", token)
	srv := server.New(srvCfg, serverStore)
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// Clients
	client1Store := setupDB(t, "client1_skew")
	client1Sync := setupSyncService(t, client1Store, ts.URL, token, t.TempDir())

	client2Store := setupDB(t, "client2_skew")
	client2Sync := setupSyncService(t, client2Store, ts.URL, token, t.TempDir())

	// 1. Establish baseline
	err := client2Sync.SyncNow(ctx)
	require.NoError(t, err)

	// 2. Client 1 creates a note with past timestamp
	past := time.Now().Add(-24 * time.Hour)
	note := api.Entry{ID: "past_note", Title: "Past Note", Body: "Body", CreatedAt: past, UpdatedAt: past}
	_, err = client1Store.Entries.CreateEntry(ctx, note)
	require.NoError(t, err)

	// 3. Sync
	err = client1Sync.SyncNow(ctx)
	require.NoError(t, err)

	err = client2Sync.SyncNow(ctx)
	require.NoError(t, err)

	// Verify Client 2 has the note
	c2n, err := client2Store.Entries.GetEntry(ctx, "past_note")
	require.NoError(t, err)
	require.Equal(t, "Past Note", c2n.Title)
}
