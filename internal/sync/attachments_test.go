package sync_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/internal/notes"
	"github.com/mithrel/ginkgo/internal/server"
	"github.com/mithrel/ginkgo/pkg/api"
)

func TestAttachmentsSyncWithNamespaceEncryptionAndRetry(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "encrypted"}[encrypted], func(t *testing.T) {
			ctx := context.Background()
			serverDir, clientDir, receiverDir := t.TempDir(), t.TempDir(), t.TempDir()
			serverStore, clientStore, receiverStore := setupDB(t, "server"), setupDB(t, "client"), setupDB(t, "receiver")
			cfg := viper.New()
			cfg.Set("auth.token", "test-token")
			cfg.Set("data_dir", serverDir)
			router := server.New(cfg, serverStore).Router()
			var puts atomic.Int32
			var unavailable atomic.Bool
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/v1/attachments/") {
					if r.Method == http.MethodPut {
						puts.Add(1)
					}
					if r.Method == http.MethodGet && unavailable.Load() {
						http.Error(w, "missing", http.StatusNotFound)
						return
					}
				}
				router.ServeHTTP(w, r)
			}))
			defer host.Close()
			key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
			configure := func(v *viper.Viper) {
				v.Set("namespaces.work.e2ee", encrypted)
				v.Set("namespaces.work.key_provider", "config")
				v.Set("namespaces.work.read_key", key)
				v.Set("namespaces.work.write_key", key)
			}
			writer := setupSyncServiceWithConfig(t, clientStore, host.URL, "test-token", clientDir, configure)
			reader := setupSyncServiceWithConfig(t, receiverStore, host.URL, "test-token", receiverDir, configure)
			var pngBytes bytes.Buffer
			require.NoError(t, png.Encode(&pngBytes, image.NewNRGBA(image.Rect(0, 0, 4, 4))))
			im, err := imagecontext.Decode(pngBytes.Bytes(), time.Now(), "original screen")
			require.NoError(t, err)
			originals := imagecontext.NewStore(blobstore.NewFileStore(filepath.Join(clientDir, "attachments")))
			refs, err := originals.Save(ctx, "work", []imagecontext.Image{im})
			require.NoError(t, err)
			entry, err := notes.NewService(clientStore.Entries).Create(ctx, notes.CreateRequest{Namespace: "work", Title: "Screen", Body: "This error", OriginalText: "This error", Source: &api.Source{Images: refs}})
			require.NoError(t, err)
			require.NoError(t, writer.SyncNow(ctx))
			require.EqualValues(t, 1, puts.Load())
			remoteData, err := blobstore.NewFileStore(filepath.Join(serverDir, "replication-blobs")).Get(ctx, "work", refs[0].ID)
			require.NoError(t, err)
			if encrypted {
				require.True(t, bytes.HasPrefix(remoteData, []byte("enc_v1\n")))
				require.False(t, bytes.Contains(remoteData, im.Data))
			} else {
				require.True(t, bytes.HasSuffix(remoteData, im.Data))
			}
			unavailable.Store(true)
			require.Error(t, reader.SyncNow(ctx))
			got, err := receiverStore.Entries.GetEntry(ctx, entry.ID)
			require.NoError(t, err, "text remains available while images are pending")
			require.Equal(t, entry.Hash(), got.Hash())
			received := imagecontext.NewStore(blobstore.NewFileStore(filepath.Join(receiverDir, "attachments")))
			_, err = received.Load(ctx, "work", refs[0])
			require.ErrorIs(t, err, os.ErrNotExist)
			unavailable.Store(false)
			require.NoError(t, reader.SyncNow(ctx))
			loaded, err := received.Load(ctx, "work", refs[0])
			require.NoError(t, err)
			require.Equal(t, im.Data, loaded.Data)
			entry.Body = "Edited text"
			entry.Version++
			entry.UpdatedAt = time.Now()
			_, err = clientStore.Entries.UpdateEntryCAS(ctx, entry, entry.Version-1)
			require.NoError(t, err)
			require.NoError(t, writer.SyncNow(ctx))
			require.EqualValues(t, 1, puts.Load(), "editing a note must not reupload its image")
		})
	}
}
