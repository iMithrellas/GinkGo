package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/cli"
	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/daemon"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

func TestE2E_ImageContext(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", root)
	requests := make(chan int, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Content json.RawMessage }
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		content := request.Messages[len(request.Messages)-1].Content
		images := 0
		if len(content) > 0 && content[0] == '[' {
			var parts []struct{ Type string }
			require.NoError(t, json.Unmarshal(content, &parts))
			for _, part := range parts {
				if part.Type == "image_url" {
					images++
				}
			}
		}
		requests <- images
		draft := `{"action":"create_note","title":"Screen note","body":"An error was observed.","tags":["work"]}`
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": draft}}}})
		_, _ = w.Write(data)
	}))
	defer provider.Close()
	cfgPath := filepath.Join(root, "config.json")
	cfg := map[string]any{
		"data_dir": filepath.Join(root, "data"), "namespace": "work", "namespaces": map[string]any{"work": map[string]any{"e2ee": false}}, "http_addr": "127.0.0.1:0",
		"voice": map[string]any{"screenshot": map[string]any{"automatic": false}},
		"ai": map[string]any{"enabled": true, "provider": "vision", "providers": map[string]any{
			"vision": map[string]any{"type": "openai-compatible", "base_url": provider.URL, "model": "test", "image_input": "supported"},
			"text":   map[string]any{"type": "openai-compatible", "base_url": provider.URL, "model": "test", "image_input": "unsupported"},
		}},
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, data, 0600))
	ctx, cancel := context.WithCancel(context.Background())
	v := viper.New()
	v.SetConfigFile(cfgPath)
	require.NoError(t, config.Load(ctx, v))
	app, err := wire.BuildApp(ctx, v)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, app) }()
	t.Cleanup(func() { cancel(); <-done; _ = app.Store.Close() })
	sock, err := ipc.SocketPath()
	require.NoError(t, err)
	require.NoError(t, waitForSocket(sock, 5*time.Second))
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewNRGBA(image.Rect(0, 0, 3, 2))))
	imagePath := filepath.Join(root, "screen.png")
	require.NoError(t, os.WriteFile(imagePath, pngBytes.Bytes(), 0600))
	runNote := func(extra ...string) struct {
		Entry       api.Entry
		ImageStatus string `json:"image_status"`
	} {
		t.Helper()
		cmd := cli.NewRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetIn(strings.NewReader("Document the error in this screenshot."))
		args := []string{"--config", cfgPath, "ai", "note", "--stdin", "--new", "--output", "json"}
		cmd.SetArgs(append(args, extra...))
		require.NoError(t, cmd.Execute())
		var result struct {
			Entry       api.Entry
			ImageStatus string `json:"image_status"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &result))
		return result
	}
	first := runNote("--image", imagePath)
	require.Equal(t, 1, <-requests)
	require.Len(t, first.Entry.Images(), 1)
	_, _, err = runCLI(t, "--config", cfgPath, "context", "attach", imagePath)
	require.NoError(t, err)
	second := runNote()
	require.Equal(t, 1, <-requests)
	require.Len(t, second.Entry.Images(), 1)
	third := runNote()
	require.Equal(t, 0, <-requests)
	require.Empty(t, third.Entry.Images(), "staged context is consumed once")
	fourth := runNote("--provider", "text", "--image", imagePath)
	require.Equal(t, 0, <-requests)
	require.Len(t, fourth.Entry.Images(), 1)
	require.Contains(t, fourth.ImageStatus, "skipped")
	exportDir := filepath.Join(root, "export")
	out, _, err := runCLI(t, "--config", cfgPath, "note", "images", first.Entry.ID, "--directory", exportDir)
	require.NoError(t, err)
	saved, err := os.ReadFile(strings.TrimSpace(out))
	require.NoError(t, err)
	require.Equal(t, pngBytes.Bytes(), saved)
	status, _, err := runCLI(t, "--config", cfgPath, "context", "status")
	require.NoError(t, err)
	require.Contains(t, status, fmt.Sprintf("%d pending", 0))
}
