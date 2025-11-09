package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/wire"
)

// waitForFile polls until a file exists or timeout.
func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return os.ErrNotExist
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newTestApp(t *testing.T, dataDir string) *wire.App {
	t.Helper()
	v := viper.New()
	v.Set("data_dir", dataDir)
	v.Set("default_namespace", "test")
	// Load applies defaults and env semantics; ignore file discovery in tests.
	if err := config.Load(context.Background(), v); err != nil {
		t.Fatalf("config load: %v", err)
	}
	app, err := wire.BuildApp(context.Background(), v)
	if err != nil {
		t.Fatalf("build app: %v", err)
	}
	return app
}

func TestDaemonNoteLifecycle(t *testing.T) {
	t.Parallel()
	// Temp runtime and data dirs
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

	app := newTestApp(t, dataDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start daemon
	done := make(chan struct{})
	go func() {
		_ = Run(ctx, app)
		close(done)
	}()

	// Wait for IPC socket to appear
	sock, err := ipc.SocketPath()
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := waitForFile(sock, 2*time.Second); err != nil {
		t.Fatalf("socket not ready: %v", err)
	}

	// Create note
	add, err := ipc.Request(ctx, sock, ipc.Message{
		Name:      "note.add",
		Title:     "Test Title",
		Body:      "Body text",
		Tags:      []string{"a", "b"},
		Namespace: "test",
	})
	if err != nil {
		t.Fatalf("add request: %v", err)
	}
	if !add.OK || add.Entry == nil {
		t.Fatalf("add failed: %+v", add)
	}
	id := add.Entry.ID
	if id == "" {
		t.Fatalf("empty id")
	}

	// Show
	show, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.show", ID: id, Namespace: "test"})
	if err != nil {
		t.Fatalf("show request: %v", err)
	}
	if !show.OK || show.Entry == nil || show.Entry.Title != "Test Title" {
		t.Fatalf("show mismatch: %+v", show)
	}

	// List
	list, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.list", Namespace: "test"})
	if err != nil {
		t.Fatalf("list request: %v", err)
	}
	if !list.OK || len(list.Entries) == 0 {
		t.Fatalf("list empty: %+v", list)
	}
	found := false
	for _, e := range list.Entries {
		if e.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("id %s not in list", id)
	}

	// Edit
	edit, err := ipc.Request(ctx, sock, ipc.Message{
		Name:      "note.edit",
		ID:        id,
		IfVersion: show.Entry.Version,
		Title:     "Updated",
		Namespace: "test",
	})
	if err != nil {
		t.Fatalf("edit request: %v", err)
	}
	if !edit.OK || edit.Entry == nil || edit.Entry.Title != "Updated" {
		t.Fatalf("edit failed: %+v", edit)
	}

	// Search FTS
	search, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.search.fts", Title: "Updated", Namespace: "test"})
	if err != nil {
		t.Fatalf("search request: %v", err)
	}
	if !search.OK {
		t.Fatalf("search failed: %+v", search)
	}

	// Delete
	del, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.delete", ID: id, Namespace: "test"})
	if err != nil {
		t.Fatalf("delete request: %v", err)
	}
	if !del.OK {
		t.Fatalf("delete failed: %+v", del)
	}

	// Show after delete -> expect not OK
	show2, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.show", ID: id, Namespace: "test"})
	if err != nil {
		t.Fatalf("show2 request: %v", err)
	}
	if show2.OK {
		t.Fatalf("expected not found after delete, got: %+v", show2)
	}
}
