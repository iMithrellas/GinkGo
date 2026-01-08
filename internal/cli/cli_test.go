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
namespace = "testcli"
[namespaces.testcli]
e2ee = false
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

func TestConfigNamespaceKey(t *testing.T) {
	cancel, _, dataDir := startTestDaemon(t)
	defer cancel()

	cfgPath := writeConfigTOML(t, dataDir)

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--config", cfgPath, "config", "namespace", "key"})
	if err := root.Execute(); err != nil {
		t.Fatalf("key execute: %v\n%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "namespace: testcli") {
		t.Fatalf("missing namespace output: %q", got)
	}
	if !strings.Contains(got, "key_provider: config") {
		t.Fatalf("missing key_provider output: %q", got)
	}
	if !strings.Contains(got, "read_key: (missing)") {
		t.Fatalf("missing read_key placeholder: %q", got)
	}
	if !strings.Contains(got, "write_key: (missing)") {
		t.Fatalf("missing write_key placeholder: %q", got)
	}

	withKeys := `data_dir = "` + strings.ReplaceAll(dataDir, "\\", "\\\\") + `"
default_namespace = "testcli"
namespace = "testcli"
[namespaces.testcli]
e2ee = true
key_provider = "config"
read_key = "cmVhZA=="
write_key = "d3JpdGU="
`
	if err := os.WriteFile(cfgPath, []byte(withKeys), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	root2 := NewRootCmd()
	var out2 bytes.Buffer
	root2.SetOut(&out2)
	root2.SetErr(&out2)
	root2.SetArgs([]string{"--config", cfgPath, "config", "namespace", "key"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("key execute with keys: %v\n%s", err, out2.String())
	}
	got2 := out2.String()
	if !strings.Contains(got2, "read_key: cmVhZA==") {
		t.Fatalf("missing read_key: %q", got2)
	}
	if !strings.Contains(got2, "write_key: d3JpdGU=") {
		t.Fatalf("missing write_key: %q", got2)
	}
}

func TestImportJSON(t *testing.T) {
	cancel, _, dataDir := startTestDaemon(t)
	defer cancel()

	cfgPath := writeConfigTOML(t, dataDir)
	tmp := t.TempDir()
	inputPath := filepath.Join(tmp, "import.json")
	input := `[
  {
    "title": "Imported Title",
    "body": "Imported Body",
    "tags": ["imported", "cli"],
    "namespace": "testcli",
    "created_at": "2025-02-01T10:00:00Z",
    "updated_at": "2025-02-01T10:00:00Z"
  }
]`
	if err := os.WriteFile(inputPath, []byte(input), 0o600); err != nil {
		t.Fatalf("write import file: %v", err)
	}

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--config", cfgPath, "import", "--file", inputPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("import execute: %v\n%s", err, out.String())
	}

	root2 := NewRootCmd()
	var out2 bytes.Buffer
	root2.SetOut(&out2)
	root2.SetErr(&out2)
	root2.SetArgs([]string{"--config", cfgPath, "note", "list", "--output", "json", "--export", "--page-size", "10"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("list execute: %v\n%s", err, out2.String())
	}
	var entries []api.Entry
	if err := json.Unmarshal(out2.Bytes(), &entries); err != nil {
		t.Fatalf("decode list json: %v\n%s", err, out2.String())
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 imported entry, got %d", len(entries))
	}
	got := entries[0]
	if got.Title != "Imported Title" {
		t.Fatalf("title mismatch: %q", got.Title)
	}
	if got.Body != "Imported Body" {
		t.Fatalf("body mismatch: %q", got.Body)
	}
	if got.Namespace != "testcli" {
		t.Fatalf("namespace mismatch: %q", got.Namespace)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "cli" || got.Tags[1] != "imported" {
		t.Fatalf("tags mismatch: %v", got.Tags)
	}
}

func TestConfigNamespaceDelete(t *testing.T) {
	cancel, sock, dataDir := startTestDaemon(t)
	defer cancel()

	cfgPath := writeConfigTOML(t, dataDir)

	// Seed a note in the namespace.
	root := NewRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "note", "add", "Delete Me"})
	if err := root.Execute(); err != nil {
		t.Fatalf("add execute: %v", err)
	}

	// Delete the namespace.
	root2 := NewRootCmd()
	root2.SetArgs([]string{"--config", cfgPath, "note", "delete", "--namespace-delete", "--yes"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("delete execute: %v", err)
	}

	// Verify list is empty.
	resp, err := ipc.Request(context.Background(), sock, ipc.Message{Name: "note.list", Namespace: "testcli"})
	if err != nil {
		t.Fatalf("list execute: %v", err)
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(resp.Entries))
	}

	// Verify config section is removed.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "[namespaces.testcli]") {
		t.Fatalf("namespace config still present")
	}
}
