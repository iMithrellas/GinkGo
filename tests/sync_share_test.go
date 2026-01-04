package tests

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	gcrypto "github.com/mithrel/ginkgo/internal/crypto"
	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/server"
	"github.com/mithrel/ginkgo/internal/sync"
	"github.com/mithrel/ginkgo/pkg/api"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func setupSyncDB(t *testing.T, name string) *db.Store {
	t.Helper()
	dir := t.TempDir()
	dsn := "sqlite://" + filepath.Join(dir, name+".db")
	s, err := db.Open(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func setupSyncServiceWithConfig(t *testing.T, s *db.Store, remoteURL, token, dataDir string, configure func(*viper.Viper)) *sync.Service {
	t.Helper()
	v := viper.New()
	v.Set("data_dir", dataDir)
	v.Set("remotes.origin.url", remoteURL)
	v.Set("remotes.origin.enabled", true)
	v.Set("remotes.origin.token", token)
	v.Set("sync.batch_size", 10)
	if configure != nil {
		configure(v)
	}
	return sync.New(v, s)
}

func TestSyncSharedNamespaceTrustedSigners(t *testing.T) {
	ctx := context.Background()
	token := "test-token"

	pub1, priv1, err := gcrypto.NewSignerKeypair()
	require.NoError(t, err)
	signerID1 := gcrypto.SignerID(pub1)
	signerPub1 := base64.StdEncoding.EncodeToString(pub1)
	signerPriv1 := base64.StdEncoding.EncodeToString(priv1)

	pub2, priv2, err := gcrypto.NewSignerKeypair()
	require.NoError(t, err)
	signerID2 := gcrypto.SignerID(pub2)
	signerPub2 := base64.StdEncoding.EncodeToString(pub2)
	signerPriv2 := base64.StdEncoding.EncodeToString(priv2)

	serverStore := setupSyncDB(t, "server_share")
	srvCfg := viper.New()
	srvCfg.Set("auth.token", token)
	srvCfg.Set("namespaces.shared.trusted_signers", []string{signerID1, signerID2})
	srv := server.New(srvCfg, serverStore)
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x55
	}
	keyB64 := base64.StdEncoding.EncodeToString(key)

	configureClient := func(v *viper.Viper, signerPub, signerPriv, origin string) {
		v.Set("namespaces.shared.e2ee", true)
		v.Set("namespaces.shared.key_provider", "config")
		v.Set("namespaces.shared.read_key", keyB64)
		v.Set("namespaces.shared.write_key", keyB64)
		v.Set("namespaces.shared.signer_key_provider", "config")
		v.Set("namespaces.shared.signer_pub", signerPub)
		v.Set("namespaces.shared.signer_priv", signerPriv)
		v.Set("namespaces.shared.origin_label", origin)
	}

	client1Store := setupSyncDB(t, "client1_share")
	client1Sync := setupSyncServiceWithConfig(t, client1Store, ts.URL, token, t.TempDir(), func(v *viper.Viper) {
		configureClient(v, signerPub1, signerPriv1, "client1")
	})

	client2Store := setupSyncDB(t, "client2_share")
	client2Sync := setupSyncServiceWithConfig(t, client2Store, ts.URL, token, t.TempDir(), func(v *viper.Viper) {
		configureClient(v, signerPub2, signerPriv2, "client2")
	})

	note1 := api.Entry{
		ID:        "shared_note_1",
		Title:     "Shared 1",
		Body:      "Body 1",
		Namespace: "shared",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_, err = client1Store.Entries.CreateEntry(ctx, note1)
	require.NoError(t, err)

	note2 := api.Entry{
		ID:        "shared_note_2",
		Title:     "Shared 2",
		Body:      "Body 2",
		Namespace: "shared",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_, err = client2Store.Entries.CreateEntry(ctx, note2)
	require.NoError(t, err)

	err = client1Sync.SyncNow(ctx)
	require.NoError(t, err)

	evs, _, err := serverStore.Events.List(ctx, api.Cursor{}, 100)
	require.NoError(t, err)
	require.Len(t, evs, 1)
	require.Equal(t, "enc_v1", evs[0].PayloadType)
	require.Equal(t, signerID1, evs[0].SignerID)

	err = client2Sync.SyncNow(ctx)
	require.NoError(t, err)

	evs, _, err = serverStore.Events.List(ctx, api.Cursor{}, 100)
	require.NoError(t, err)
	require.Len(t, evs, 2)

	seen := map[string]string{}
	for _, ev := range evs {
		seen[ev.ID] = ev.SignerID
	}
	require.Equal(t, signerID1, seen["shared_note_1"])
	require.Equal(t, signerID2, seen["shared_note_2"])

	err = client1Sync.SyncNow(ctx)
	require.NoError(t, err)

	err = client2Sync.SyncNow(ctx)
	require.NoError(t, err)

	got1, err := client1Store.Entries.GetEntry(ctx, "shared_note_2")
	require.NoError(t, err)
	require.Equal(t, "Shared 2", got1.Title)

	got2, err := client2Store.Entries.GetEntry(ctx, "shared_note_1")
	require.NoError(t, err)
	require.Equal(t, "Shared 1", got2.Title)
}
