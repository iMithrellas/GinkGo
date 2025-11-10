package editor

import "testing"

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
