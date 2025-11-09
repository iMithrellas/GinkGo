package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/daemon"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

// helper to start daemon with isolated dirs and return ctx cancel and socket path
func startTestDaemon(t *testing.T) (context.CancelFunc, string, string) {
	t.Helper()
	tmp := t.TempDir()
	runtimeDir := filepath.Join(tmp, "run")
	dataDir := filepath.Join(tmp, "data")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	v := viper.New()
	v.Set("data_dir", dataDir)
	v.Set("default_namespace", "testcli")
	v.Set("http_addr", "127.0.0.1:0") // avoid port collisions across packages
	if err := config.Load(context.Background(), v); err != nil {
		t.Fatalf("config load: %v", err)
	}
	app, err := wire.BuildApp(context.Background(), v)
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = daemon.Run(ctx, app) }()
	// Wait for socket
	sock, err := ipc.SocketPath()
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket not ready: %s", sock)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cancel, sock, dataDir
}

func writeConfigTOML(t *testing.T, dir string) string {
	t.Helper()
	cfg := filepath.Join(dir, "config.toml")
	content := `data_dir = "` + strings.ReplaceAll(dir, "\\", "\\\\") + `"
default_namespace = "testcli"
`
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfg
}

func TestCLIAddShowDeleteJSON(t *testing.T) {
	cancel, _, dataDir := startTestDaemon(t)
	defer cancel()

	cfgPath := writeConfigTOML(t, dataDir)

	// Add
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--config", cfgPath, "note", "add", "CLI Title", "-t", "work,personal"})
	if err := root.Execute(); err != nil {
		t.Fatalf("add execute: %v\n%s", err, out.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 0 {
		t.Fatalf("no output from add")
	}
	parts := strings.Split(lines[len(lines)-1], "\t")
	if len(parts) < 2 {
		t.Fatalf("unexpected add output: %q", lines[len(lines)-1])
	}
	id := parts[0]
	if id == "" {
		t.Fatalf("empty id from add")
	}

	// Show JSON
	root2 := NewRootCmd()
	var out2 bytes.Buffer
	root2.SetOut(&out2)
	root2.SetErr(&out2)
	root2.SetArgs([]string{"--config", cfgPath, "note", "show", id, "--output", "json"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("show execute: %v\n%s", err, out2.String())
	}
	var e api.Entry
	if err := json.Unmarshal(out2.Bytes(), &e); err != nil {
		t.Fatalf("decode show json: %v\n%s", err, out2.String())
	}
	if e.ID != id || e.Title != "CLI Title" {
		t.Fatalf("show mismatch: got id=%q title=%q", e.ID, e.Title)
	}

	// Delete
	root3 := NewRootCmd()
	var out3 bytes.Buffer
	root3.SetOut(&out3)
	root3.SetErr(&out3)
	root3.SetArgs([]string{"--config", cfgPath, "note", "delete", id})
	if err := root3.Execute(); err != nil {
		t.Fatalf("delete execute: %v\n%s", err, out3.String())
	}
	// Verify deletion by attempting to show again - should error
	root4 := NewRootCmd()
	var out4 bytes.Buffer
	root4.SetOut(&out4)
	root4.SetErr(&out4)
	root4.SetArgs([]string{"--config", cfgPath, "note", "show", id})
	if err := root4.Execute(); err == nil {
		t.Fatalf("expected show to fail after delete, output=%q", out4.String())
	}
}
