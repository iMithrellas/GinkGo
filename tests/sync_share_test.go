package tests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
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

func TestSyncEncryptedOfflineExpansionHistory(t *testing.T) {
	ctx := context.Background()
	const token = "test-token"
	pub, priv, err := gcrypto.NewSignerKeypair()
	require.NoError(t, err)
	serverStore := setupSyncDB(t, "server_offline")
	serverCfg := viper.New()
	serverCfg.Set("auth.token", token)
	serverCfg.Set("namespaces.shared.trusted_signers", []string{gcrypto.SignerID(pub)})
	ts := httptest.NewServer(server.New(serverCfg, serverStore).Router())
	defer ts.Close()
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	configure := func(v *viper.Viper) {
		v.Set("namespaces.shared.e2ee", true)
		v.Set("namespaces.shared.key_provider", "config")
		v.Set("namespaces.shared.read_key", key)
		v.Set("namespaces.shared.write_key", key)
		v.Set("namespaces.shared.signer_key_provider", "config")
		v.Set("namespaces.shared.signer_pub", base64.StdEncoding.EncodeToString(pub))
		v.Set("namespaces.shared.signer_priv", base64.StdEncoding.EncodeToString(priv))
	}
	first := setupSyncDB(t, "first_offline")
	second := setupSyncDB(t, "second_offline")
	firstDir, secondDir := t.TempDir(), t.TempDir()
	firstSync := setupSyncServiceWithConfig(t, first, ts.URL, token, firstDir, configure)
	secondSync := setupSyncServiceWithConfig(t, second, ts.URL, token, secondDir, configure)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	unrelated, err := second.Entries.CreateEntry(ctx, api.Entry{
		ID: "unrelated", Namespace: "shared", Title: "Other note", Body: "other content", CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	initial, err := first.Entries.CreateEntry(ctx, api.Entry{
		ID: "offline-note", Version: 1, Namespace: "shared", Title: "Offline note", Body: "first cleaned body",
		CreatedAt: now, UpdatedAt: now,
		Provenance: &api.Provenance{Sources: []api.Source{{ID: "A", CapturedAt: now, Kind: "text", OriginalText: "original A"}}},
	})
	require.NoError(t, err)
	expansion := initial
	expansion.Version++
	expansion.Body = "expanded cleaned body"
	expansion.UpdatedAt = now.Add(time.Minute)
	expansion.Provenance = &api.Provenance{Sources: append(append([]api.Source(nil), initial.Provenance.Sources...), api.Source{
		ID: "B", CapturedAt: now.Add(time.Minute), Kind: "text", OriginalText: "original B",
	})}
	expanded, err := first.Entries.UpdateEntryCAS(ctx, expansion, initial.Version)
	require.NoError(t, err)
	// Seed an unrelated event before the first client pulls its own A then A+B
	// history. A rolled-back batch must not be mistaken for successful progress.
	require.NoError(t, secondSync.SyncNow(ctx))
	require.NoError(t, firstSync.SyncNow(ctx))
	events, _, err := serverStore.Events.List(ctx, api.Cursor{}, 100)
	require.NoError(t, err)
	require.Len(t, events, 3)
	for _, ev := range events {
		require.Equal(t, "enc_v1", ev.PayloadType)
	}
	assertCursor := func(dir string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, "sync", "cursor_origin.json"))
		require.NoError(t, err)
		var cursor struct {
			PullAfter time.Time `json:"pull_after"`
		}
		require.NoError(t, json.Unmarshal(data, &cursor))
		require.True(t, cursor.PullAfter.Equal(events[len(events)-1].Time), "pull cursor must advance through the complete history")
	}
	assertCursor(firstDir)
	gotOther, err := first.Entries.GetEntry(ctx, unrelated.ID)
	require.NoError(t, err)
	require.Equal(t, unrelated, gotOther, "unrelated event in stale-history batch must commit")

	// Both devices sync again: the second materializes A -> A+B, and the first
	// has already advanced beyond its own historical events, rather than stalling.
	require.NoError(t, secondSync.SyncNow(ctx))
	require.NoError(t, firstSync.SyncNow(ctx))
	for _, store := range []*db.Store{first, second} {
		got, err := store.Entries.GetEntry(ctx, expanded.ID)
		require.NoError(t, err)
		require.Equal(t, expanded, got)
		got, err = store.Entries.GetEntry(ctx, unrelated.ID)
		require.NoError(t, err)
		require.Equal(t, unrelated, got)
	}
	assertCursor(firstDir)
	assertCursor(secondDir)
	events, _, err = serverStore.Events.List(ctx, api.Cursor{}, 100)
	require.NoError(t, err)
	require.Len(t, events, 3, "replayed history must not produce new events")
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
		Provenance: &api.Provenance{Sources: []api.Source{{
			ID: "source-voice", CapturedAt: time.Now().UTC(), Kind: "voice", OriginalText: "unmodified transcript",
			Language: "en", STTEngine: "whisper.cpp", STTModel: "base",
		}}},
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
	require.Equal(t, note1.Provenance, got2.Provenance)

	// Expansion on another device appends a second original and returns both
	// sources through the same encrypted, signed replication path.
	got2.Provenance.Sources = append(got2.Provenance.Sources, api.Source{
		ID: "source-text", CapturedAt: time.Now().UTC(), Kind: "text", OriginalText: "additional input",
	})
	previousVersion := got2.Version
	got2.Version++
	got2.Body = "Expanded cleaned body"
	got2.UpdatedAt = time.Now().UTC()
	expanded, err := client2Store.Entries.UpdateEntryCAS(ctx, got2, previousVersion)
	require.NoError(t, err)
	require.NoError(t, client2Sync.SyncNow(ctx))
	require.NoError(t, client1Sync.SyncNow(ctx))
	received, err := client1Store.Entries.GetEntry(ctx, got2.ID)
	require.NoError(t, err)
	require.Equal(t, expanded.Provenance, received.Provenance)
	require.Equal(t, expanded.Body, received.Body)
	require.Equal(t, expanded.Hash(), received.Hash())
}
