package notes

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/pkg/api"
)

func TestCreateSourceMetadataCannotReplaceOriginalOrIdentity(t *testing.T) {
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(store.Entries)
	for _, kind := range []string{"voice", "text", "", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			metadata := &api.Source{ID: "untrusted-id", CapturedAt: time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), Kind: kind, OriginalText: "untrusted original", Language: "cs", STTEngine: "whisper", STTModel: "tiny"}
			before := time.Now()
			entry, err := service.Create(context.Background(), CreateRequest{Namespace: "voice", Body: "rendered", OriginalText: " exact original ", Source: metadata})
			if err != nil {
				t.Fatal(err)
			}
			source := entry.Provenance.Sources[0]
			expectedKind := "text"
			if kind == "voice" {
				expectedKind = "voice"
			}
			if source.Kind != expectedKind || source.ID == "" || source.ID == metadata.ID || source.OriginalText != " exact original " || source.CapturedAt.Before(before) || source.CapturedAt.After(time.Now()) || source.Language != metadata.Language || source.STTEngine != metadata.STTEngine || source.STTModel != metadata.STTModel {
				t.Fatalf("invalid source capture: %#v", source)
			}
		})
	}
}

func TestAppendDatedSectionPreservesBodyAndGroupsSameMinute(t *testing.T) {
	firstTime := time.Date(2026, 10, 16, 23, 30, 0, 0, time.FixedZone("UTC-7", -7*60*60))
	first := appendDatedSection("Existing body\nwith exact spacing  ", "First addition", firstTime)
	wantHeading := "## 2026-10-17 06:30 UTC"
	if !strings.HasPrefix(first, "Existing body\nwith exact spacing  \n\n"+wantHeading+"\n\nFirst addition") {
		t.Fatalf("unexpected dated append: %q", first)
	}
	second := appendDatedSection(first, "Second addition", firstTime.Add(30*time.Second))
	if strings.Count(second, wantHeading) != 1 || !strings.HasSuffix(second, "First addition\n\nSecond addition") {
		t.Fatalf("same-minute addition should share its subsection: %q", second)
	}
	nextMinute := appendDatedSection(second, "Third addition", firstTime.Add(time.Minute))
	if !strings.HasSuffix(nextMinute, "## 2026-10-17 06:31 UTC\n\nThird addition") {
		t.Fatalf("new minute should get a new subsection: %q", nextMinute)
	}
}

func TestAppendDatedSectionDoesNotReuseHeadingInsideCodeFence(t *testing.T) {
	at := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	body := "Example:\n```md\n## 2026-10-16\n```"
	got := appendDatedSection(body, "Addition", at)
	if !strings.HasSuffix(got, "\n\n## 2026-10-16 00:00 UTC\n\nAddition") {
		t.Fatalf("code sample heading was treated as a section: %q", got)
	}
}
