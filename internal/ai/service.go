package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/notes"
	"github.com/mithrel/ginkgo/pkg/api"
)

// NoteRequest supplies original input and application-controlled overrides.
type NoteRequest struct {
	Text, Namespace, Title, Action, NoteID string
	Tags                                   []string
	// Source contains trusted application capture metadata, never model output.
	// The notes service generates source identity/time and uses Text as original.
	Source *api.Source
}

// NoteResult describes a committed creation, expansion, or raw fallback.
type NoteResult struct {
	Entry          api.Entry `json:"entry"`
	Action         string    `json:"action"`
	FallbackReason string    `json:"fallback_reason,omitempty"`
}

// ServiceOptions bounds inference input/context independently of provider limits.
type ServiceOptions struct {
	Enabled          bool
	DefaultNamespace string
	DefaultTags      []string
	ContextLimit     int
	MaxInputBytes    int
	MaxContextBytes  int
	SystemPrompt     string
}

// Service coordinates one bounded inference at a time with ordinary note writes.
type Service struct {
	repo    db.EntryRepo
	notes   *notes.Service
	resolve func(string) (Provider, error)
	options ServiceOptions
	slot    chan struct{}
}

// NewService accepts app.Store.Entries; it performs no provider initialization.
// Nonpositive limits use defaults; context count is capped at 100 and byte
// budgets at 1 MiB each. The daemon owns synchronization after a result.
func NewService(repo db.EntryRepo, resolve func(string) (Provider, error), options ServiceOptions) *Service {
	if options.DefaultNamespace == "" {
		options.DefaultNamespace = "default"
	}
	if options.ContextLimit <= 0 {
		options.ContextLimit = 8
	}
	if options.ContextLimit > 100 {
		options.ContextLimit = 100
	}
	if options.MaxInputBytes <= 0 {
		options.MaxInputBytes = 64 << 10
	}
	if options.MaxInputBytes > 1<<20 {
		options.MaxInputBytes = 1 << 20
	}
	if options.MaxContextBytes <= 0 {
		options.MaxContextBytes = 64 << 10
	}
	if options.MaxContextBytes > 1<<20 {
		options.MaxContextBytes = 1 << 20
	}
	options.DefaultTags = append([]string(nil), options.DefaultTags...)
	if options.SystemPrompt == "" {
		options.SystemPrompt = DefaultNoteSystemPrompt
	}
	return &Service{repo: repo, notes: notes.NewService(repo), resolve: resolve, options: options, slot: make(chan struct{}, 1)}
}

// providerFallbackReason deliberately never interpolates err.Error(). Providers,
// wrappers and resolvers can return untrusted text containing secrets or input.
// Only sentinel identity selects an application-owned diagnostic.
func providerFallbackReason(err error) string {
	switch {
	case errors.Is(err, ErrGenerationRestricted):
		return "AI model's free-tier policy rejects generation-only use; choose a provider/model supporting tool-free generation (tools remain disabled)"
	case errors.Is(err, ErrOpenCodeConsole):
		return "OpenCode saved Console credentials are incompatible with generation-only isolation; use a separate profile without Console or another provider"
	case errors.Is(err, ErrAuthentication):
		return "AI authentication failed; check the selected provider's saved login or API-key setup"
	case errors.Is(err, ErrModelUnavailable):
		return "AI model unavailable; check the configured provider/model ID and account access"
	case errors.Is(err, ErrRateLimited):
		return "AI provider rate limited the request; retry later"
	case errors.Is(err, ErrProviderUnavailable):
		return "AI provider temporarily unavailable; check provider status or retry later"
	case errors.Is(err, context.DeadlineExceeded):
		return "AI provider timed out; check provider connectivity or increase the provider timeout"
	case errors.Is(err, context.Canceled):
		return "AI provider canceled generation while the note request remained active; check provider availability"
	case errors.Is(err, ErrInputLimit):
		return "AI request exceeds the provider input limit; reduce note context or increase the configured limit"
	case errors.Is(err, ErrOutputLimit):
		return "AI provider output exceeds the configured limit"
	case errors.Is(err, ErrProtocol):
		return "AI provider returned an incompatible response; check adapter/provider version compatibility"
	default:
		return "AI provider failed; check provider configuration, model access and connectivity"
	}
}

func (s *Service) raw(ctx context.Context, request NoteRequest, reason string) (NoteResult, error) {
	if err := ctx.Err(); err != nil {
		return NoteResult{}, err
	}
	title := request.Title
	if strings.TrimSpace(title) == "" {
		title = notes.RawTitle(request.Text)
	}
	entry, err := s.notes.Create(ctx, notes.CreateRequest{Namespace: request.Namespace, Title: title, Body: request.Text, OriginalText: request.Text, Tags: append(append([]string(nil), s.options.DefaultTags...), request.Tags...), Source: request.Source})
	if err != nil {
		return NoteResult{}, err
	}
	return NoteResult{Entry: entry, Action: "create_note", FallbackReason: reason}, nil
}

// Note validates model proposals and commits through the existing repository.
// All non-cancellation AI/validation/CAS failures preserve input as a new raw
// note. Empty input is an error. Expansion is append-only, not an AI rewrite.
func (s *Service) Note(ctx context.Context, request NoteRequest, providerName string) (NoteResult, error) {
	if err := ctx.Err(); err != nil {
		return NoteResult{}, err
	}
	if strings.TrimSpace(request.Text) == "" {
		return NoteResult{}, fmt.Errorf("note input is empty")
	}
	if request.Namespace == "" {
		request.Namespace = s.options.DefaultNamespace
	}
	if request.Action == "" {
		request.Action = "auto"
	}
	if !s.options.Enabled {
		return s.raw(ctx, request, "AI is disabled")
	}
	if len(request.Text) > s.options.MaxInputBytes {
		return s.raw(ctx, request, "input exceeds AI byte budget")
	}
	if request.Action != "auto" && request.Action != "create" && request.Action != "expand" {
		return s.raw(ctx, request, "invalid requested action")
	}
	if (request.Action == "expand" && request.NoteID == "") || (request.Action == "create" && request.NoteID != "") {
		return s.raw(ctx, request, "invalid expansion target")
	}
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	case <-ctx.Done():
		return NoteResult{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return NoteResult{}, err
	}
	candidates, err := s.context(ctx, request)
	if err != nil {
		if cancel := ctx.Err(); cancel != nil {
			return NoteResult{}, cancel
		}
		return s.raw(ctx, request, "note context unavailable or exceeds budget")
	}
	if s.resolve == nil {
		return s.raw(ctx, request, "AI provider unavailable; check configured provider name and settings")
	}
	hints := s.tagHints(ctx, request)
	if err := ctx.Err(); err != nil {
		return NoteResult{}, err
	}
	provider, err := s.resolve(providerName)
	if cancel := ctx.Err(); cancel != nil {
		return NoteResult{}, cancel
	}
	if err != nil || provider == nil {
		return s.raw(ctx, request, providerFallbackReason(err))
	}
	// Share only the source kind, not capture metadata or earlier originals.
	inputKind := "text"
	if request.Source != nil && request.Source.Kind == "voice" {
		inputKind = "voice"
	}
	prompt, _ := json.Marshal(struct {
		Input     string        `json:"input"`
		InputKind string        `json:"input_kind"`
		Action    string        `json:"action"`
		TargetID  string        `json:"target_id,omitempty"`
		Notes     []noteContext `json:"notes"`
		Tags      tagHints      `json:"tag_context"`
	}{request.Text, inputKind, request.Action, request.NoteID, candidates.views, hints})
	response, err := provider.Complete(ctx, Request{System: s.options.SystemPrompt, Prompt: string(prompt)})
	// Only the request's context controls whether saving is forbidden. An
	// adapter's shorter local timeout is a provider failure, not cancellation
	// of the incoming note, and must preserve its text through raw fallback.
	if cancel := ctx.Err(); cancel != nil {
		return NoteResult{}, cancel
	}
	if err != nil {
		return s.raw(ctx, request, providerFallbackReason(err))
	}
	draft, err := s.validateDraft(response.Text, request, candidates.entries)
	if err != nil {
		return s.raw(ctx, request, "AI returned an invalid note draft; expected the requested strict JSON note format")
	}
	if err := ctx.Err(); err != nil {
		return NoteResult{}, err
	}
	var entry api.Entry
	title := draft.Title
	if request.Title != "" {
		title = request.Title
	}
	if draft.Action == "create_note" {
		tags := append(append(append([]string(nil), s.options.DefaultTags...), request.Tags...), draft.Tags...)
		entry, err = s.notes.Create(ctx, notes.CreateRequest{Namespace: request.Namespace, Title: title, Body: draft.Body, Tags: tags, OriginalText: request.Text, Source: request.Source})
	} else {
		entry, err = s.notes.ExpandCASWithSource(ctx, candidates.entries[draft.TargetID], title, draft.Body, append(append([]string(nil), request.Tags...), draft.Tags...), request.Text, request.Source)
		if err != nil {
			if cancel := ctx.Err(); cancel != nil {
				return NoteResult{}, cancel
			}
			return s.raw(ctx, request, "note expansion failed or conflicted; target unchanged by AI")
		}
	}
	if err != nil {
		return NoteResult{}, err
	}
	return NoteResult{Entry: entry, Action: draft.Action}, nil
}

const (
	maxKnownTags    = 40
	maxTagHintBytes = 4 << 10
)

type tagHints struct {
	Known    []string `json:"known_tags"`
	Explicit []string `json:"explicit_tags"`
	Defaults []string `json:"default_tags"`
}

// tagHints has an independent 4 KiB JSON budget. Lookup failure is best effort;
// it cannot bypass final merged-tag validation or introduce unscoped fallback.
func (s *Service) tagHints(ctx context.Context, request NoteRequest) tagHints {
	hints := tagHints{Known: []string{}, Explicit: []string{}, Defaults: []string{}}
	add := func(destination *[]string, names []string, limit int) {
		for _, name := range notes.NormalizeTags(names) {
			if len(*destination) >= limit {
				break
			}
			if len(name) > 128 {
				continue
			}
			*destination = append(*destination, name)
			encoded, _ := json.Marshal(hints)
			if len(encoded) > maxTagHintBytes {
				*destination = (*destination)[:len(*destination)-1]
			}
		}
	}
	add(&hints.Explicit, request.Tags, 64)
	if request.NoteID == "" && request.Action != "expand" {
		add(&hints.Defaults, s.options.DefaultTags, 64)
	}
	if vocabulary, ok := s.repo.(db.TagVocabulary); ok {
		if names, err := vocabulary.RankedTags(ctx, request.Namespace, request.Text, time.Now().UTC(), maxKnownTags); err == nil {
			add(&hints.Known, names, maxKnownTags)
		}
	}
	return hints
}

type noteContext struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags,omitempty"`
}

type candidateContext struct {
	views   []noteContext
	entries map[string]api.Entry
}

func (s *Service) context(ctx context.Context, request NoteRequest) (candidateContext, error) {
	result := candidateContext{views: []noteContext{}, entries: map[string]api.Entry{}}
	if s.options.MaxContextBytes < 2 {
		return result, fmt.Errorf("context budget cannot fit an empty candidate array")
	}
	var entries []api.Entry
	if request.NoteID != "" {
		entry, err := s.notes.ScopedGet(ctx, request.NoteID, request.Namespace)
		if err != nil {
			return result, err
		}
		entries = []api.Entry{entry}
	} else if request.Action != "create" {
		var err error
		entries, _, err = s.repo.ListEntries(ctx, api.ListQuery{Namespace: request.Namespace, Limit: s.options.ContextLimit, IncludeBody: true})
		if err != nil {
			return result, err
		}
	}
	for _, entry := range entries {
		if len(result.views) >= s.options.ContextLimit {
			break
		}
		if entry.Namespace != request.Namespace {
			continue
		}
		view := noteContext{ID: entry.ID, Title: entry.Title, Body: entry.Body, Tags: entry.Tags}
		proposed := append(append([]noteContext(nil), result.views...), view)
		encoded, err := json.Marshal(proposed)
		if err != nil {
			return result, err
		}
		if len(encoded) > s.options.MaxContextBytes {
			if request.NoteID != "" {
				return result, fmt.Errorf("explicit target exceeds context budget")
			}
			continue
		}
		result.views = proposed
		result.entries[entry.ID] = entry
	}
	return result, nil
}

type noteDraft struct {
	Action   string   `json:"action"`
	TargetID string   `json:"target_id,omitempty"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Tags     []string `json:"tags,omitempty"`
}

func (s *Service) validateDraft(text string, request NoteRequest, candidates map[string]api.Entry) (noteDraft, error) {
	var draft noteDraft
	if len(text) > s.options.MaxInputBytes+s.options.MaxContextBytes {
		return draft, ErrOutputLimit
	}
	// encoding/json accepts duplicate keys; reject them before decoding the draft.
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return draft, ErrProtocol
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return draft, ErrProtocol
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return draft, ErrProtocol
		}
		seen[name] = true
		if decoder.Decode(new(json.RawMessage)) != nil {
			return draft, ErrProtocol
		}
	}
	if _, err = decoder.Token(); err != nil {
		return draft, ErrProtocol
	}
	if decoder.Decode(new(any)) != io.EOF {
		return draft, ErrProtocol
	}
	if decodeObject([]byte(text), &draft) != nil {
		return draft, ErrProtocol
	}
	if strings.TrimSpace(draft.Title) == "" || utf8.RuneCountInString(draft.Title) > 256 || strings.TrimSpace(draft.Body) == "" || len(draft.Tags) > 64 {
		return draft, ErrProtocol
	}
	for _, tag := range draft.Tags {
		if len(tag) > 128 {
			return draft, ErrProtocol
		}
	}
	switch draft.Action {
	case "create_note":
		if draft.TargetID != "" || request.Action == "expand" || request.NoteID != "" {
			return draft, ErrProtocol
		}
	case "expand_note":
		if request.Action == "create" {
			return draft, ErrProtocol
		}
		if _, ok := candidates[draft.TargetID]; !ok || draft.TargetID == "" {
			return draft, ErrProtocol
		}
		if request.NoteID != "" && draft.TargetID != request.NoteID {
			return draft, ErrProtocol
		}
	default:
		return draft, ErrProtocol
	}
	// Match the notes service's actual merge semantics, including legacy tags.
	tags := append([]string(nil), request.Tags...)
	if draft.Action == "create_note" {
		tags = append(tags, s.options.DefaultTags...)
	} else {
		tags = append(tags, candidates[draft.TargetID].Tags...)
	}
	tags = append(tags, draft.Tags...)
	if len(notes.NormalizeTags(tags)) == 0 {
		return draft, ErrProtocol
	}
	return draft, nil
}
