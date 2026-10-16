package notes

import (
	"context"
	"path/filepath"
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
