package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/pkg/api"
)

func seedNotes(t *testing.T, sock string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		_, err := ipc.Request(context.Background(), sock, ipc.Message{
			Name:      "note.add",
			Title:     fmt.Sprintf("Note %d", i),
			Tags:      []string{"work"},
			Namespace: "testcli",
		})
		if err != nil {
			t.Fatalf("seed note %d: %v", i, err)
		}
	}
}

func TestListAndSearchPaging(t *testing.T) {
	cancel, sock, dataDir := startTestDaemon(t)
	defer cancel()
	cfgPath := writeConfigTOML(t, dataDir)
	seedNotes(t, sock, 5)

	// list with small page size
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--config", cfgPath, "note", "list", "--namespace", "testcli", "--output", "json", "--page-size", "2"})
	if err := root.Execute(); err != nil {
		t.Fatalf("list execute: %v\n%s", err, out.String())
	}
	var entries []api.Entry
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("decode list json: %v\n%s", err, out.String())
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	// search with small page size
	root2 := NewRootCmd()
	var out2 bytes.Buffer
	root2.SetOut(&out2)
	root2.SetErr(&out2)
	root2.SetArgs([]string{"--config", cfgPath, "note", "search", "--namespace", "testcli", "fts", "note", "--output", "json", "--page-size", "2"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("search execute: %v\n%s", err, out2.String())
	}
	var found []api.Entry
	if err := json.Unmarshal(out2.Bytes(), &found); err != nil {
		t.Fatalf("decode search json: %v\n%s", err, out2.String())
	}
	if len(found) != 5 {
		t.Fatalf("expected 5 search results, got %d", len(found))
	}
}
