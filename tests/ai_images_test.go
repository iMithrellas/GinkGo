package tests

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/cli"
	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/daemon"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

// This crosses the real CLI, IPC, daemon, HTTP adapter and original store. The
// HTTP endpoint is a deterministic fake; it is not an authenticated model test.
func TestE2E_AIImageRetentionAndExport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		support   string
		malformed bool
	}{
		{name: "vision", support: "supported"},
		{name: "text-only", support: "unsupported"},
		{name: "raw-fallback", support: "supported", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan []byte, 4)
			nextRequest := func() []byte {
				t.Helper()
				select {
				case request := <-requests:
					return request
				case <-time.After(3 * time.Second):
					t.Fatal("provider request was not submitted")
					return nil
				}
			}
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
				if err != nil {
					http.Error(w, "read failed", http.StatusBadRequest)
					return
				}
				requests <- body
				text := `{"action":"create_note","title":"A visual note","body":"Organized text","tags":["journal"]}`
				if tc.malformed {
					text = `{"action":"create_note"`
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
					"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text},
				}}})
			}))
			defer host.Close()

			dir := t.TempDir()
			runDir := filepath.Join(dir, "run")
			require.NoError(t, os.Mkdir(runDir, 0700))
			t.Setenv("XDG_RUNTIME_DIR", runDir)
			cfgPath := filepath.Join(dir, "config.json")
			settings := map[string]any{
				"data_dir": filepath.Join(dir, "data"), "namespace": "work",
				"http_addr":  "127.0.0.1:0",
				"namespaces": map[string]any{"work": map[string]bool{"e2ee": false}, "other": map[string]bool{"e2ee": false}},
				"voice":      map[string]any{"enabled": false, "screenshot": map[string]bool{"automatic": false}},
				"remotes":    map[string]any{"origin": map[string]bool{"enabled": false}},
				"ai": map[string]any{"enabled": true, "provider": "fixture", "providers": map[string]any{
					"fixture": map[string]string{"type": "openai-compatible", "base_url": host.URL + "/v1", "model": "fixture-model", "image_input": tc.support},
				}},
			}
			encoded, err := json.Marshal(settings)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfgPath, encoded, 0600))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := viper.New()
			cfg.SetConfigFile(cfgPath)
			require.NoError(t, config.Load(ctx, cfg))
			app, err := wire.BuildApp(ctx, cfg)
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { done <- daemon.Run(ctx, app) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("daemon shutdown did not join")
					return
				}
				require.NoError(t, app.Store.Close())
			})
			socket, err := ipc.SocketPath()
			require.NoError(t, err)
			require.NoError(t, waitForSocket(socket, 5*time.Second))
			run := func(input string, args ...string) (string, error) {
				cmd := cli.NewRootCmd()
				cmd.SetIn(strings.NewReader(input))
				var out, diagnostics bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&diagnostics)
				cmd.SetArgs(append([]string{"--config", cfgPath}, args...))
				err := cmd.Execute()
				return out.String(), err
			}

			var originals [][]byte
			var paths []string
			for i, c := range []color.NRGBA{{R: 255, A: 255}, {G: 255, A: 255}} {
				im := image.NewNRGBA(image.Rect(0, 0, 2, 2))
				im.SetNRGBA(0, 0, c)
				var data bytes.Buffer
				require.NoError(t, png.Encode(&data, im))
				path := filepath.Join(dir, []string{"red.png", "green.png"}[i])
				require.NoError(t, os.WriteFile(path, data.Bytes(), 0600))
				originals = append(originals, bytes.Clone(data.Bytes()))
				paths = append(paths, path)
			}
			// Direct images consume and replace staged images, even with capture off.
			_, err = run("", "context", "attach", paths[1])
			require.NoError(t, err)
			original := "  Explain these attached screenshots.\nKeep this exact original.\n\n"
			out, err := run(original, "ai", "note", "--stdin", "--new", "--image", paths[0], "--image", paths[1], "--output", "json")
			require.NoError(t, err)
			var result ai.NoteResult
			require.NoError(t, json.Unmarshal([]byte(out), &result))
			require.Contains(t, result.ImageStatus, "retained")
			require.Len(t, result.Entry.Images(), 2)
			require.Equal(t, original, result.Entry.Provenance.Sources[0].OriginalText)
			if tc.malformed {
				require.NotEmpty(t, result.FallbackReason)
				require.Equal(t, original, result.Entry.Body)
			} else {
				require.Empty(t, result.FallbackReason)
			}
			var request struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			require.NoError(t, json.Unmarshal(nextRequest(), &request))
			require.Len(t, request.Messages, 2)
			if tc.support == "supported" {
				var parts []struct {
					Type     string `json:"type"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				}
				require.NoError(t, json.Unmarshal(request.Messages[1].Content, &parts))
				require.Len(t, parts, 3)
				for i, data := range originals {
					require.Equal(t, "image_url", parts[i+1].Type)
					require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data), parts[i+1].ImageURL.URL)
				}
			} else {
				var text string
				require.NoError(t, json.Unmarshal(request.Messages[1].Content, &text))
				require.Contains(t, result.ImageStatus, "skipped")
			}

			out, err = run("", "note", "list", "--output", "json", "--export")
			require.NoError(t, err)
			var exported []api.Entry
			require.NoError(t, json.Unmarshal([]byte(out), &exported))
			require.Len(t, exported, 1)
			require.Equal(t, result.Entry.Provenance, exported[0].Provenance)
			imageDir := filepath.Join(dir, "exported-images")
			_, err = run("", "note", "images", result.Entry.ID, "--directory", imageDir)
			require.NoError(t, err)
			for i, ref := range result.Entry.Images() {
				data, err := os.ReadFile(filepath.Join(imageDir, ref.ID+".png"))
				require.NoError(t, err)
				require.Equal(t, originals[i], data)
			}
			_, err = run("", "note", "images", result.Entry.ID, "--namespace", "other")
			require.Error(t, err, "image originals must remain namespace-scoped")

			// Neither a consumed staged image nor historical attachments may be resent.
			_, err = run("A later unrelated thought.", "ai", "note", "--stdin", "--output", "json")
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(nextRequest(), &request))
			var text string
			require.NoError(t, json.Unmarshal(request.Messages[1].Content, &text))
			require.NotContains(t, text, "data:image/")
		})
	}
}
