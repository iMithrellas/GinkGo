package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamespaceFromURI(t *testing.T) {
	uri := "file:///tmp/workspace.ns123.ginkgo.md"
	if got := namespaceFromURI(uri); got != "workspace" {
		t.Fatalf("namespaceFromURI=%q want %q", got, "workspace")
	}

	uri = "file:///tmp/noprefix.ginkgo.md"
	if got := namespaceFromURI(uri); got != "" {
		t.Fatalf("namespaceFromURI=%q want empty", got)
	}
}

func TestShouldCompleteTags(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.ginkgo.md")
	content := strings.Join([]string{
		"# GinkGo Note",
		"Tags: alpha, beta",
		"---",
		"body",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp note: %v", err)
	}

	uri := "file://" + path
	tagsParams := completionParams{
		TextDocument: textDocumentIdentifier{URI: uri},
		Position:     position{Line: 1, Character: 3},
	}
	if !shouldCompleteTags(tagsParams) {
		t.Fatalf("shouldCompleteTags returned false on tags line")
	}

	otherParams := completionParams{
		TextDocument: textDocumentIdentifier{URI: uri},
		Position:     position{Line: 0, Character: 1},
	}
	if shouldCompleteTags(otherParams) {
		t.Fatalf("shouldCompleteTags returned true on non-tags line")
	}
}
