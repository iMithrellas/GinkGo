package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/pkg/api"
)

type noteProviderFunc func(context.Context, Request) (Response, error)

func (f noteProviderFunc) Complete(ctx context.Context, request Request) (Response, error) {
	return f(ctx, request)
}

func noteTestRepo(t *testing.T) db.EntryRepo {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.Entries
}

func seedNote(t *testing.T, repo db.EntryRepo, id, ns, body string) api.Entry {
	t.Helper()
	entry, err := repo.CreateEntry(context.Background(), api.Entry{ID: id, Version: 1, Namespace: ns, Title: "Existing title", Body: body, Tags: []string{"old"}, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func noteCount(t *testing.T, repo db.EntryRepo) int {
	t.Helper()
	entries, _, err := repo.ListEntries(context.Background(), api.ListQuery{Limit: 100, IncludeBody: true})
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func fixedNoteService(repo db.EntryRepo, text string, options ServiceOptions) *Service {
	return NewService(repo, func(string) (Provider, error) {
		return noteProviderFunc(func(context.Context, Request) (Response, error) { return Response{Text: text}, nil }), nil
	}, options)
}

func assertSource(t *testing.T, entry api.Entry, index int, text string) {
	t.Helper()
	if entry.Provenance == nil || len(entry.Provenance.Sources) <= index {
		t.Fatal("missing source")
	}
	source := entry.Provenance.Sources[index]
	if source.OriginalText != text || source.ID == "" || source.CapturedAt.IsZero() || source.Kind != "text" {
		t.Fatalf("invalid provenance source: %#v", source)
	}
}

func TestNoteDisabledPreservesExactRawInput(t *testing.T) {
	repo := noteTestRepo(t)
	calls := 0
	service := NewService(repo, func(string) (Provider, error) { calls++; return nil, errors.New("must not resolve") }, ServiceOptions{DefaultNamespace: "personal", DefaultTags: []string{" Default "}})
	text := "  original\ntext with spacing  \n"
	result, err := service.Note(context.Background(), NoteRequest{Text: text, Tags: []string{" Extra ", "default"}}, "unused")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || result.Entry.Body != text || result.Entry.Namespace != "personal" || result.FallbackReason == "" || result.Action != "create_note" {
		t.Fatalf("invalid disabled result: %#v calls=%d", result, calls)
	}
	if !reflect.DeepEqual(result.Entry.Tags, []string{"default", "extra"}) {
		t.Fatal(result.Entry.Tags)
	}
	assertSource(t, result.Entry, 0, text)
}

func TestNoteCreateAndOverrides(t *testing.T) {
	repo := noteTestRepo(t)
	service := fixedNoteService(repo, `{"action":"create_note","title":"Model title","body":"Clean Markdown","tags":["new"]}`, ServiceOptions{Enabled: true, DefaultNamespace: "personal", DefaultTags: []string{"default"}})
	result, err := service.Note(context.Background(), NoteRequest{Text: " exact original ", Title: "Explicit", Action: "create", Tags: []string{"user"}}, "provider")
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.Title != "Explicit" || result.Entry.Body != "Clean Markdown" || result.FallbackReason != "" || result.Entry.Version != 1 {
		t.Fatalf("bad create: %#v", result)
	}
	assertSource(t, result.Entry, 0, " exact original ")
}

func TestNoteExpansionAppendsAndSnapshotsBaselineOnce(t *testing.T) {
	repo := noteTestRepo(t)
	initial := seedNote(t, repo, "target", "personal", " Existing body\nwith exact spacing ")
	service := NewService(repo, func(string) (Provider, error) {
		return noteProviderFunc(func(_ context.Context, request Request) (Response, error) {
			if !strings.Contains(request.System, "APPEND ONLY") {
				t.Error("missing append-only provider contract")
			}
			return Response{Text: `{"action":"expand_note","target_id":"target","title":"Expanded","body":"New material","tags":["new","OLD"]}`}, nil
		}), nil
	}, ServiceOptions{Enabled: true, DefaultNamespace: "personal"})
	for index, input := range []string{" first input ", "second\ninput"} {
		request := NoteRequest{Text: input, Action: "expand", NoteID: "target", Tags: []string{"user"}}
		if index == 0 {
			request.Action = "auto"
			request.NoteID = ""
		}
		result, err := service.Note(context.Background(), request, "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Action != "expand_note" || result.FallbackReason != "" || result.Entry.ID != initial.ID || result.Entry.Namespace != initial.Namespace || !result.Entry.CreatedAt.Equal(initial.CreatedAt) || result.Entry.Version != int64(index+2) {
			t.Fatalf("bad expansion: %#v", result)
		}
		if !strings.HasPrefix(result.Entry.Body, initial.Body+"\n\nNew material") {
			t.Fatal("old body lost")
		}
		if len(result.Entry.Provenance.Sources) != index+2 {
			t.Fatal("baseline duplicated")
		}
		assertSource(t, result.Entry, 0, initial.Body)
		assertSource(t, result.Entry, index+1, input)
		if !reflect.DeepEqual(result.Entry.Tags, []string{"old", "user", "new"}) {
			t.Fatal(result.Entry.Tags)
		}
	}
}

func TestNoteWrongNamespaceAndMissingTargetFallbackBeforeProvider(t *testing.T) {
	for _, id := range []string{"private-target", "missing"} {
		t.Run(id, func(t *testing.T) {
			repo := noteTestRepo(t)
			seedNote(t, repo, "private-target", "other", "private content")
			calls := 0
			service := NewService(repo, func(string) (Provider, error) { calls++; return nil, nil }, ServiceOptions{Enabled: true, DefaultNamespace: "personal"})
			result, err := service.Note(context.Background(), NoteRequest{Text: "preserve me", NoteID: id, Action: "expand"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 || result.FallbackReason == "" || result.Entry.Namespace != "personal" || result.Entry.Body != "preserve me" || result.Entry.ID == id {
				t.Fatalf("unsafe missing/scoped target: %#v", result)
			}
		})
	}
}

func TestNoteInvalidDraftFallback(t *testing.T) {
	for _, draft := range []string{
		`{"action":"delete_note","title":"x","body":"x"}`,
		`{"action":"create_note","title":"x","body":"x","namespace":"other"}`,
		`{"action":"create_note","title":"x","body":"x","version":999}`,
		`{"action":"create_note","title":"x","body":"x","body":"duplicate"}`,
		`{"action":"expand_note","target_id":"invented","title":"x","body":"x"}`,
		`{"action":"create_note","title":"x"}`,
		`{"action":"create_note","title":"x","body":"x"} {}`,
	} {
		t.Run(draft, func(t *testing.T) {
			repo := noteTestRepo(t)
			service := fixedNoteService(repo, draft, ServiceOptions{Enabled: true})
			result, err := service.Note(context.Background(), NoteRequest{Text: "exact raw"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if result.FallbackReason == "" || result.Entry.Body != "exact raw" {
				t.Fatalf("bad fallback: %#v", result)
			}
			assertSource(t, result.Entry, 0, "exact raw")
		})
	}
}

func TestNoteBadRequestActionAndProviderFailure(t *testing.T) {
	for _, action := range []string{"invalid", "auto"} {
		t.Run(action, func(t *testing.T) {
			repo := noteTestRepo(t)
			service := NewService(repo, func(string) (Provider, error) { return nil, errors.New("secret provider error") }, ServiceOptions{Enabled: true})
			result, err := service.Note(context.Background(), NoteRequest{Text: "raw", Action: action}, "")
			if err != nil {
				t.Fatal(err)
			}
			if result.FallbackReason == "" || strings.Contains(result.FallbackReason, "secret") {
				t.Fatal(result)
			}
		})
	}
}

func TestNoteConcurrentCASCreatesRawFallback(t *testing.T) {
	repo := noteTestRepo(t)
	initial := seedNote(t, repo, "target", "personal", "baseline")
	service := NewService(repo, func(string) (Provider, error) {
		return noteProviderFunc(func(ctx context.Context, _ Request) (Response, error) {
			edit := initial
			edit.Version++
			edit.Body = "concurrent human edit"
			if _, err := repo.UpdateEntryCAS(ctx, edit, initial.Version); err != nil {
				t.Error(err)
			}
			return Response{Text: `{"action":"expand_note","target_id":"target","title":"x","body":"AI addition"}`}, nil
		}), nil
	}, ServiceOptions{Enabled: true, DefaultNamespace: "personal"})
	result, err := service.Note(context.Background(), NoteRequest{Text: " exact new input ", NoteID: "target"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.FallbackReason == "" || result.Entry.ID == "target" || result.Entry.Body != " exact new input " {
		t.Fatal(result)
	}
	target, err := repo.GetEntry(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	if target.Body != "concurrent human edit" || target.Version != 2 || target.Provenance != nil {
		t.Fatal("concurrent target overwritten")
	}
	if noteCount(t, repo) != 2 {
		t.Fatal("fallback not stored")
	}
}

func TestNoteCancellationNeverFallsBack(t *testing.T) {
	repo := noteTestRepo(t)
	started := make(chan struct{})
	service := NewService(repo, func(string) (Provider, error) {
		return noteProviderFunc(func(ctx context.Context, _ Request) (Response, error) {
			close(started)
			<-ctx.Done()
			return Response{}, ctx.Err()
		}), nil
	}, ServiceOptions{Enabled: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := service.Note(ctx, NoteRequest{Text: "must not save"}, ""); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if noteCount(t, repo) != 0 {
		t.Fatal("cancelled input was saved")
	}
	_, err := service.Note(context.Background(), NoteRequest{Text: " \n\t "}, "")
	if err == nil || noteCount(t, repo) != 0 {
		t.Fatal("empty input saved")
	}
}

func TestNoteProviderLocalCancellationPreservesInput(t *testing.T) {
	for _, providerError := range []error{context.DeadlineExceeded, context.Canceled} {
		t.Run(providerError.Error(), func(t *testing.T) {
			repo := noteTestRepo(t)
			service := NewService(repo, func(string) (Provider, error) {
				return noteProviderFunc(func(context.Context, Request) (Response, error) {
					return Response{}, providerError
				}), nil
			}, ServiceOptions{Enabled: true})
			const text = "  exact input\nwith trailing whitespace  \n"
			result, err := service.Note(context.Background(), NoteRequest{Text: text}, "")
			if err != nil {
				t.Fatal(err)
			}
			if result.FallbackReason == "" || result.Entry.Body != text || noteCount(t, repo) != 1 {
				t.Fatalf("provider-local cancellation lost input: %#v", result)
			}
			assertSource(t, result.Entry, 0, text)
		})
	}
}

func TestNoteCallerDeadlineNeverFallsBack(t *testing.T) {
	repo := noteTestRepo(t)
	service := NewService(repo, func(string) (Provider, error) {
		return noteProviderFunc(func(ctx context.Context, _ Request) (Response, error) {
			<-ctx.Done()
			return Response{}, ctx.Err()
		}), nil
	}, ServiceOptions{Enabled: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := service.Note(ctx, NoteRequest{Text: "must not save"}, "")
	if !errors.Is(err, context.DeadlineExceeded) || noteCount(t, repo) != 0 {
		t.Fatalf("caller deadline must not save a fallback: %v", err)
	}
}

func TestNoteContextBudgetAndPrivacy(t *testing.T) {
	repo := noteTestRepo(t)
	seedNote(t, repo, "huge", "personal", strings.Repeat("x", 2000))
	small := seedNote(t, repo, "small", "personal", "short body")
	small.Version++
	small.Provenance = &api.Provenance{Sources: []api.Source{{ID: "original", Kind: "text", OriginalText: "never-send-original", CapturedAt: time.Now()}}}
	if _, err := repo.UpdateEntryCAS(context.Background(), small, 1); err != nil {
		t.Fatal(err)
	}
	seedNote(t, repo, "other", "other", "never-send-namespace")
	service := NewService(repo, func(string) (Provider, error) {
		return noteProviderFunc(func(_ context.Context, request Request) (Response, error) {
			var prompt struct {
				Notes json.RawMessage `json:"notes"`
			}
			if err := json.Unmarshal([]byte(request.Prompt), &prompt); err != nil {
				t.Error(err)
			}
			if len(prompt.Notes) > 160 || strings.Contains(request.Prompt, "never-send") || strings.Contains(request.Prompt, "provenance") {
				t.Error("context budget/scope/provenance leaked")
			}
			return Response{Text: `{"action":"create_note","title":"x","body":"new","tags":["topic"]}`}, nil
		}), nil
	}, ServiceOptions{Enabled: true, DefaultNamespace: "personal", ContextLimit: 2, MaxContextBytes: 160})
	result, err := service.Note(context.Background(), NoteRequest{Text: "input"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.FallbackReason != "" {
		t.Fatal(result)
	}
	result, err = service.Note(context.Background(), NoteRequest{Text: "exact fallback", NoteID: "huge"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.FallbackReason == "" || result.Entry.Body != "exact fallback" {
		t.Fatal(result)
	}
}

func TestNoteInferenceSlotCancellation(t *testing.T) {
	repo := noteTestRepo(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	service := NewService(repo, func(string) (Provider, error) {
		return noteProviderFunc(func(context.Context, Request) (Response, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return Response{Text: `{"action":"create_note","title":"x","body":"new"}`}, nil
		}), nil
	}, ServiceOptions{Enabled: true})
	first := make(chan error, 1)
	go func() { _, err := service.Note(context.Background(), NoteRequest{Text: "first"}, ""); first <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.Note(ctx, NoteRequest{Text: "second"}, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || noteCount(t, repo) != 1 {
		t.Fatal("cancelled waiter ran inference or saved")
	}
}

func voiceMetadata() *api.Source {
	return &api.Source{ID: "do-not-trust-id", CapturedAt: time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), Kind: "voice", OriginalText: "do-not-trust-original", Language: "cs", STTEngine: "whisper.cpp", STTModel: "small"}
}

func assertVoiceSource(t *testing.T, entry api.Entry, index int, text string, after time.Time) {
	t.Helper()
	if entry.Provenance == nil || len(entry.Provenance.Sources) <= index {
		t.Fatal("missing voice source")
	}
	source := entry.Provenance.Sources[index]
	if source.Kind != "voice" || source.Language != "cs" || source.STTEngine != "whisper.cpp" || source.STTModel != "small" || source.OriginalText != text || source.ID == "" || source.ID == "do-not-trust-id" || source.CapturedAt.Before(after) || source.CapturedAt.After(time.Now()) {
		t.Fatalf("invalid application-generated voice source: %#v", source)
	}
}

func TestVoiceRawFallbackPreservesCaptureMetadata(t *testing.T) {
	for _, mode := range []string{"disabled", "provider-failure", "invalid-draft"} {
		t.Run(mode, func(t *testing.T) {
			repo := noteTestRepo(t)
			calls := 0
			service := NewService(repo, func(string) (Provider, error) {
				calls++
				if mode == "provider-failure" {
					return nil, errors.New("provider unavailable")
				}
				return noteProviderFunc(func(context.Context, Request) (Response, error) { return Response{Text: `{"bad":"draft"}`}, nil }), nil
			}, ServiceOptions{Enabled: mode != "disabled", DefaultNamespace: "voice"})
			before := time.Now()
			metadata := voiceMetadata()
			original := *metadata
			const text = "  Exact spoken transcript\nwith spacing  "
			result, err := service.Note(context.Background(), NoteRequest{Text: text, Source: metadata}, "")
			if err != nil {
				t.Fatal(err)
			}
			if result.FallbackReason == "" || result.Entry.Body != text {
				t.Fatalf("invalid raw voice fallback: %#v", result)
			}
			if mode == "disabled" && calls != 0 {
				t.Fatal("AI-disabled voice resolved provider")
			}
			assertVoiceSource(t, result.Entry, 0, text, before)
			if *metadata != original {
				t.Fatal("caller metadata mutated")
			}
		})
	}
}

func TestVoiceAICreatePreservesTranscriptSeparately(t *testing.T) {
	repo := noteTestRepo(t)
	const original = "  use post gress 16 and cube er netties; keep db_host unchanged.\n"
	const corrected = "Use PostgreSQL 16 and Kubernetes; keep db_host unchanged."
	service := fixedNoteService(repo, `{"action":"create_note","title":"PostgreSQL and Kubernetes","body":"Use PostgreSQL 16 and Kubernetes; keep db_host unchanged.","tags":["postgresql","kubernetes"]}`, ServiceOptions{Enabled: true})
	before := time.Now()
	result, err := service.Note(context.Background(), NoteRequest{Text: original, Action: "create", Source: voiceMetadata()}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.Body != corrected || result.FallbackReason != "" {
		t.Fatal(result)
	}
	stored, err := repo.GetEntry(context.Background(), result.Entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Body != corrected {
		t.Fatal("corrected note body was not stored")
	}
	assertVoiceSource(t, stored, 0, original, before)
}

func TestVoiceExpansionRetainsTextBaselineAndEarlierSources(t *testing.T) {
	repo := noteTestRepo(t)
	initial := seedNote(t, repo, "voice-target", "voice", " Existing spelling stays: post gress ")
	const addition = "Use PostgreSQL for the database."
	service := fixedNoteService(repo, `{"action":"expand_note","target_id":"voice-target","title":"PostgreSQL additions","body":"Use PostgreSQL for the database."}`, ServiceOptions{Enabled: true, DefaultNamespace: "voice"})
	var previous []api.Source
	for index, input := range []string{" use post gress for the database ", " use postgress for the database "} {
		before := time.Now()
		result, err := service.Note(context.Background(), NoteRequest{Text: input, Action: "expand", NoteID: initial.ID, Source: voiceMetadata()}, "")
		if err != nil {
			t.Fatal(err)
		}
		if result.FallbackReason != "" {
			t.Fatal(result)
		}
		if result.Entry.Body != initial.Body+strings.Repeat("\n\n"+addition, index+1) {
			t.Fatal("terminology correction rewrote existing body or lost new material")
		}
		assertSource(t, result.Entry, 0, initial.Body)
		assertVoiceSource(t, result.Entry, index+1, input, before)
		if len(result.Entry.Provenance.Sources) != index+2 {
			t.Fatal("source history lost or baseline duplicated")
		}
		if previous != nil && !reflect.DeepEqual(previous, result.Entry.Provenance.Sources[:len(previous)]) {
			t.Fatal("earlier voice sources changed")
		}
		previous = append([]api.Source(nil), result.Entry.Provenance.Sources...)
	}
}

func TestNotePromptSharesInputKindWithoutCaptureMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source *api.Source
		kind   string
	}{
		{"typed input", nil, "text"},
		{"voice transcript", voiceMetadata(), "voice"},
		{"unknown kind", &api.Source{Kind: "untrusted-kind", OriginalText: "private-original", STTModel: "/private/model.bin"}, "text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := noteTestRepo(t)
			const original = "  post gress database\n"
			calls := 0
			service := NewService(repo, func(string) (Provider, error) {
				return noteProviderFunc(func(_ context.Context, request Request) (Response, error) {
					calls++
					var prompt struct {
						Input     string `json:"input"`
						InputKind string `json:"input_kind"`
					}
					if err := json.Unmarshal([]byte(request.Prompt), &prompt); err != nil {
						t.Fatal(err)
					}
					if prompt.Input != original || prompt.InputKind != tc.kind {
						t.Fatalf("unexpected provider input: %#v", prompt)
					}
					for _, private := range []string{"do-not-trust", "private-original", "/private/model.bin", "untrusted-kind", "stt_model", "provenance"} {
						if strings.Contains(request.Prompt, private) {
							t.Fatalf("capture metadata leaked: %s", private)
						}
					}
					return Response{Text: `{"action":"create_note","title":"PostgreSQL","body":"PostgreSQL database","tags":["postgresql"]}`}, nil
				}), nil
			}, ServiceOptions{Enabled: true})
			result, err := service.Note(context.Background(), NoteRequest{Text: original, Action: "create", Source: tc.source}, "")
			if err != nil || result.FallbackReason != "" || calls != 1 {
				t.Fatalf("note request failed: calls=%d result=%#v err=%v", calls, result, err)
			}
		})
	}
}

func TestNoteSystemPromptIsConfigurableAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "default", want: DefaultNoteSystemPrompt},
		{name: "configured", prompt: "Use our team's terminology guide.", want: "Use our team's terminology guide."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := noteTestRepo(t)
			service := NewService(repo, func(string) (Provider, error) {
				return noteProviderFunc(func(_ context.Context, request Request) (Response, error) {
					if request.System != tc.want {
						t.Fatalf("wrong system prompt: %q", request.System)
					}
					return Response{Text: `{"action":"create_note","title":"Title","body":"Body","tags":["topic"]}`}, nil
				}), nil
			}, ServiceOptions{Enabled: true, SystemPrompt: tc.prompt})
			result, err := service.Note(context.Background(), NoteRequest{Text: "input", Action: "create"}, "")
			if err != nil || result.FallbackReason != "" {
				t.Fatalf("note failed: result=%#v err=%v", result, err)
			}
		})
	}
}

func TestCancelledVoiceRequestNeverSaves(t *testing.T) {
	repo := noteTestRepo(t)
	service := NewService(repo, nil, ServiceOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.Note(ctx, NoteRequest{Text: "cancelled transcript", Source: voiceMetadata()}, "")
	if !errors.Is(err, context.Canceled) || noteCount(t, repo) != 0 {
		t.Fatalf("cancelled voice request saved: %v", err)
	}
}

func TestNoteSafeProviderDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{ErrGenerationRestricted, "free-tier policy"},
		{ErrOpenCodeConsole, "separate profile without Console"},
		{ErrAuthentication, "authentication failed"},
		{ErrModelUnavailable, "model unavailable"},
		{ErrProviderUnavailable, "temporarily unavailable"},
		{ErrRateLimited, "rate limited"},
		{context.DeadlineExceeded, "timed out"},
		{context.Canceled, "request remained active"},
		{ErrInputLimit, "input limit"},
		{ErrOutputLimit, "output exceeds"},
		{ErrProtocol, "incompatible response"},
		{errors.New("secret-auth-and-prompt"), "check provider configuration"},
	} {
		for _, stage := range []string{"resolve", "complete"} {
			t.Run(tc.reason+"/"+stage, func(t *testing.T) {
				repo := noteTestRepo(t)
				failure := fmt.Errorf("secret-auth-and-prompt: %w", tc.err)
				service := NewService(repo, func(string) (Provider, error) {
					if stage == "resolve" {
						return nil, failure
					}
					return noteProviderFunc(func(context.Context, Request) (Response, error) { return Response{}, failure }), nil
				}, ServiceOptions{Enabled: true})
				const text = " exact original voice transcript \n"
				before := time.Now()
				result, err := service.Note(context.Background(), NoteRequest{Text: text, Source: voiceMetadata()}, "")
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(result.FallbackReason, tc.reason) || strings.Contains(result.FallbackReason, "secret-auth-and-prompt") {
					t.Fatal("unsafe or unhelpful fallback diagnostic")
				}
				if result.Entry.Body != text || noteCount(t, repo) != 1 {
					t.Fatal("raw fallback lost")
				}
				assertVoiceSource(t, result.Entry, 0, text, before)
			})
		}
	}
}
