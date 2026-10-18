package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/pkg/api"
)

func contextImagePath(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 3, 2))))
	path := filepath.Join(t.TempDir(), "screen.png")
	require.NoError(t, os.WriteFile(path, b.Bytes(), 0600))
	return path
}

func TestNoteImageExportPreservesOversizedOriginal(t *testing.T) {
	data, err := os.ReadFile(contextImagePath(t))
	require.NoError(t, err)
	data = append(data, make([]byte, (4<<20)+1-len(data))...)
	original, err := imagecontext.Decode(data, time.Unix(123, 0), "synthetic original")
	require.NoError(t, err)
	store := imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))
	refs, err := store.Save(context.Background(), "work", []imagecontext.Image{original})
	require.NoError(t, err)
	hash := sha256.Sum256(data)
	require.Equal(t, hex.EncodeToString(hash[:]), refs[0].SHA256)
	entry := api.Entry{ID: "synthetic-note", Namespace: "work", Body: "Generated note", Provenance: &api.Provenance{Sources: []api.Source{{Images: refs}}}}
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, Entry: &entry, ImageStatus: "retained originals; included reduced-size copies in AI request"})
	cmd := newNoteImagesCmd()
	cmd.SetContext(ctx)
	getApp(cmd).Images = store
	directory := t.TempDir()
	cmd.SetArgs([]string{entry.ID, "--directory", directory})
	cmd.SetOut(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "note.show", (<-messages).Name)
	exported, err := os.ReadFile(filepath.Join(directory, refs[0].ID+".png"))
	require.NoError(t, err)
	require.Equal(t, hash, sha256.Sum256(exported))
	require.Equal(t, data, exported)
}

func TestExplicitContextCommandsWithoutAutomaticCapture(t *testing.T) {
	for _, action := range []string{"attach", "status", "clear"} {
		t.Run(action, func(t *testing.T) {
			ctx, messages := startAICommandFake(t, ipc.Response{OK: true, Msg: "ready"})
			cmd := newContextCmd()
			cmd.SetContext(ctx)
			var out bytes.Buffer
			cmd.SetOut(&out)
			args := []string{action}
			if action == "attach" {
				args = append(args, contextImagePath(t))
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			msg := <-messages
			want := "context." + action
			if action == "attach" {
				want = "context.stage"
				require.Len(t, msg.Images, 1)
				require.Equal(t, 3, msg.Images[0].Width)
			}
			require.Equal(t, want, msg.Name)
			require.Equal(t, "work", msg.Namespace)
			require.Equal(t, "ready\n", out.String())
		})
	}
}

func TestAINoteExplicitImageIPC(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, Entry: &api.Entry{ID: "note"}, ImageStatus: "retained; skipped"})
	cmd := newAICmd()
	cmd.SetContext(ctx)
	cmd.SetIn(strings.NewReader("Explain the attached error"))
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs([]string{"note", "--stdin", "--image", contextImagePath(t), "--output", "json"})
	require.NoError(t, cmd.Execute())
	msg := <-messages
	require.Equal(t, "ai.note", msg.Name)
	require.Len(t, msg.Images, 1)
	require.Contains(t, out.String(), `"image_status":"retained; skipped"`)
	require.Empty(t, diagnostics.String())
}
