// Package notes owns small, namespace-scoped note mutations and provenance.
package notes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/pkg/api"
)

// Service performs note mutations without triggering synchronization.
type Service struct{ repo db.EntryRepo }

// NewService uses the application's existing entry repository.
func NewService(repo db.EntryRepo) *Service { return &Service{repo: repo} }

// CreateRequest separates rendered content from its exact original input.
type CreateRequest struct {
	Namespace, Title, Body, OriginalText string
	Tags                                 []string
	// Source supplies application-captured kind and STT metadata only. IDs,
	// timestamps and original text are always constructed by this service.
	Source *api.Source
}

// NormalizeTags trims, lowercases, and deduplicates tags in input order.
func NormalizeTags(tags []string) []string {
	var result []string
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}
	return result
}

// RawTitle derives a bounded deterministic title without altering the input.
func RawTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			runes := []rune(line)
			if len(runes) > 80 {
				runes = runes[:80]
			}
			return string(runes)
		}
	}
	return "Note"
}

func source(text string, now time.Time, metadata *api.Source) api.Source {
	result := api.Source{ID: api.NewID(), CapturedAt: now, Kind: "text", OriginalText: text}
	if metadata != nil {
		if metadata.Kind == "voice" {
			result.Kind = "voice"
		}
		result.Language = metadata.Language
		result.STTEngine = metadata.STTEngine
		result.STTModel = metadata.STTModel
	}
	return result
}

// Create atomically stores the new entry and its input provenance via the repo.
func (s *Service) Create(ctx context.Context, request CreateRequest) (api.Entry, error) {
	if err := ctx.Err(); err != nil {
		return api.Entry{}, err
	}
	now := time.Now().UTC()
	entry := api.Entry{ID: api.NewID(), Version: 1, Namespace: request.Namespace, Title: request.Title, Body: request.Body, Tags: NormalizeTags(request.Tags), CreatedAt: now, UpdatedAt: now, Provenance: &api.Provenance{Sources: []api.Source{source(request.OriginalText, now, request.Source)}}}
	result, err := s.repo.CreateEntry(ctx, entry)
	if err != nil {
		return api.Entry{}, fmt.Errorf("create note: %w", err)
	}
	return result, nil
}

// ScopedGet deliberately conceals entries outside the selected namespace.
func (s *Service) ScopedGet(ctx context.Context, id, namespace string) (api.Entry, error) {
	entry, err := s.repo.GetEntry(ctx, id)
	if err != nil {
		return api.Entry{}, fmt.Errorf("get note: %w", err)
	}
	if entry.Namespace != namespace {
		return api.Entry{}, db.ErrNotFound
	}
	return entry, nil
}

// ExpandCAS appends new material, never replaces prior body content. It preserves
// identity, namespace, creation time, tags and sources, snapshots a legacy body
// as the first baseline, then adds the exact contributing input. No CAS retry is
// performed; the caller decides how to preserve input after a conflict.
func (s *Service) ExpandCAS(ctx context.Context, current api.Entry, title, addition string, tags []string, input string) (api.Entry, error) {
	return s.ExpandCASWithSource(ctx, current, title, addition, tags, input, nil)
}

// ExpandCASWithSource retains ExpandCAS's append-only semantics and captures
// optional application-provided voice/STT metadata for the new input only.
// Existing originals and any new text baseline are never relabeled as voice.
func (s *Service) ExpandCASWithSource(ctx context.Context, current api.Entry, title, addition string, tags []string, input string, metadata *api.Source) (api.Entry, error) {
	if err := ctx.Err(); err != nil {
		return api.Entry{}, err
	}
	updated := current
	if title != "" {
		updated.Title = title
	}
	updated.Body = current.Body
	if updated.Body != "" {
		updated.Body += "\n\n"
	}
	updated.Body += addition
	updated.Tags = NormalizeTags(append(append([]string(nil), current.Tags...), tags...))
	updated.Version = current.Version + 1
	updated.UpdatedAt = time.Now().UTC()
	updated.Provenance = &api.Provenance{}
	if current.Provenance != nil {
		updated.Provenance.Sources = append([]api.Source(nil), current.Provenance.Sources...)
	}
	if len(updated.Provenance.Sources) == 0 {
		updated.Provenance.Sources = append(updated.Provenance.Sources, source(current.Body, updated.UpdatedAt, nil))
	}
	updated.Provenance.Sources = append(updated.Provenance.Sources, source(input, updated.UpdatedAt, metadata))
	result, err := s.repo.UpdateEntryCAS(ctx, updated, current.Version)
	if err != nil {
		return api.Entry{}, fmt.Errorf("expand note: %w", err)
	}
	return result, nil
}
