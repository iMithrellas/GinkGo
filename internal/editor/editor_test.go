package editor

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEditedNote(t *testing.T) {
	input := `# comment line
Title: My Title
Tags: alpha, beta ,  gamma
---
Body line 1
Body line 2
`
	title, tags, body := ParseEditedNote(input)
	if title != "My Title" {
		t.Fatalf("title=%q", title)
	}
	wantTags := []string{"alpha", "beta", "gamma"}
	if len(tags) != len(wantTags) {
		t.Fatalf("tags len=%d want %d (%v)", len(tags), len(wantTags), tags)
	}
	for i := range wantTags {
		if tags[i] != wantTags[i] {
			t.Fatalf("tag[%d]=%q want %q", i, tags[i], wantTags[i])
		}
	}
	if body != "Body line 1\nBody line 2" {
		t.Fatalf("body=%q", body)
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("  hello\nworld\n"); got != "hello" {
		t.Fatalf("FirstLine=%q", got)
	}
	// Long text gets truncated to 120 chars
	long := "x"
	for len(long) < 130 {
		long += "y"
	}
	fl := FirstLine(long)
	if len(fl) != 120 {
		t.Fatalf("FirstLine length=%d want 120", len(fl))
	}
}

func TestComposeContent(t *testing.T) {
	content := ComposeContent("Title", []string{"alpha", "beta"}, "body")
	if !strings.Contains(content, "Title: Title") {
		t.Fatalf("expected title line, got %q", content)
	}
	if !strings.Contains(content, "Tags: alpha, beta") {
		t.Fatalf("expected tags line, got %q", content)
	}
	if !strings.Contains(content, "---\nbody\n") {
		t.Fatalf("expected body separator, got %q", content)
	}
}

func TestPathForID(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	path, err := PathForID("note-id", "team space")
	if err != nil {
		t.Fatalf("PathForID error: %v", err)
	}

	base := filepath.Base(path)
	if base != "team%20space.note-id.ginkgo.md" {
		t.Fatalf("PathForID base=%q", base)
	}
}
