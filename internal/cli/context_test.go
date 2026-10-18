package cli

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
