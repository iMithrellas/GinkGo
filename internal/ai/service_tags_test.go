package ai

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/pkg/api"
)

func TestNoteFinalMergedTagPolicy(t *testing.T) {
	for _, tt := range []struct {
		name                         string
		expand                       bool
		existing, defaults, explicit []string
		draft                        string
		want                         []string
		fallback                     bool
	}{
		{name: "create omitted", fallback: true},
		{name: "create empty", draft: `,"tags":[]`, fallback: true},
		{name: "create whitespace", draft: `,"tags":[" ","\t"]`, fallback: true},
		{name: "create meaningful new", draft: `,"tags":[" Deployment ","deployment"," "]`, want: []string{"deployment"}},
		{name: "create default sufficient", defaults: []string{" Work "}, want: []string{"work"}},
		{name: "create explicit sufficient", explicit: []string{" User "}, draft: `,"tags":[]`, want: []string{"user"}},
		{name: "create blank defaults insufficient", defaults: []string{" "}, fallback: true},
		{name: "create merged normalized", defaults: []string{" Work "}, explicit: []string{"WORK", " User "}, draft: `,"tags":["user","Deployment"]`, want: []string{"work", "user", "deployment"}},
		{name: "malformed tag type", defaults: []string{"work"}, draft: `,"tags":"work"`, fallback: true},
		{name: "malformed array item", defaults: []string{"work"}, draft: `,"tags":[42]`, fallback: true},
		{name: "overlong model tag", defaults: []string{"work"}, draft: `,"tags":["` + strings.Repeat("x", 129) + `"]`, fallback: true},
		{name: "expand existing sufficient", expand: true, existing: []string{" Old "}, want: []string{"old"}},
		{name: "expand no tags", expand: true, fallback: true},
		{name: "expand blank existing", expand: true, existing: []string{" "}, fallback: true},
		{name: "expand defaults do not apply", expand: true, defaults: []string{"work"}, fallback: true},
		{name: "expand explicit sufficient", expand: true, explicit: []string{" User "}, want: []string{"user"}},
		{name: "expand model sufficient", expand: true, draft: `,"tags":["Deployment"]`, want: []string{"deployment"}},
		{name: "expand merge normalized", expand: true, existing: []string{" OLD ", "old"}, explicit: []string{" User "}, defaults: []string{"not-added"}, draft: `,"tags":["user","Deployment"]`, want: []string{"old", "user", "deployment"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := noteTestRepo(t)
			request := NoteRequest{Text: "  exact input\n  ", Action: "create", Tags: tt.explicit}
			action := `"action":"create_note"`
			var initial api.Entry
			if tt.expand {
				initial = seedNote(t, repo, "target", "default", "baseline")
				initial.Tags = tt.existing
				initial.Version++
				var err error
				initial, err = repo.UpdateEntryCAS(context.Background(), initial, 1)
				if err != nil {
					t.Fatal(err)
				}
				request.Action, request.NoteID = "expand", "target"
				action = `"action":"expand_note","target_id":"target"`
			}
			service := fixedNoteService(repo, `{`+action+`,"title":"Clean","body":"Cleaned"`+tt.draft+`}`, ServiceOptions{Enabled: true, DefaultTags: tt.defaults})
			result, err := service.Note(context.Background(), request, "")
			if err != nil {
				t.Fatal(err)
			}
			if (result.FallbackReason != "") != tt.fallback {
				t.Fatalf("unexpected result: %#v", result)
			}
			if tt.fallback {
				if result.Entry.Body != request.Text {
					t.Fatal("lost exact raw input")
				}
				assertSource(t, result.Entry, 0, request.Text)
				if tt.expand {
					got, _ := repo.GetEntry(context.Background(), "target")
					if !reflect.DeepEqual(got, initial) {
						t.Fatal("invalid draft modified target")
					}
				}
			} else if !reflect.DeepEqual(result.Entry.Tags, tt.want) || len(result.Entry.Tags) == 0 {
				t.Fatalf("got tags %v want %v", result.Entry.Tags, tt.want)
			}
		})
	}
}

type vocabularyRepo struct {
	db.EntryRepo
	lookup func(context.Context, string, string, time.Time, int) ([]string, error)
}

func (r vocabularyRepo) RankedTags(ctx context.Context, ns, input string, now time.Time, limit int) ([]string, error) {
	return r.lookup(ctx, ns, input, now, limit)
}

func TestTagHintsBoundedAndLookupFailure(t *testing.T) {
	for _, mode := range []string{"bounded", "failure", "cancelled", "no-capability"} {
		t.Run(mode, func(t *testing.T) {
			base := noteTestRepo(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var repo db.EntryRepo = vocabularyRepo{EntryRepo: base, lookup: func(_ context.Context, ns, input string, now time.Time, limit int) ([]string, error) {
				if ns != "selected" || input != "deployment" || now.IsZero() || limit != 40 {
					t.Fatal("bad vocabulary scope/limit")
				}
				if mode == "failure" {
					return nil, errors.New("sensitive lookup error")
				}
				if mode == "cancelled" {
					cancel()
					return nil, ctx.Err()
				}
				names := []string{" DEPLOYMENT ", "deployment", " ", strings.Repeat("x", 129)}
				for i := 0; i < 100; i++ {
					names = append(names, strings.Repeat("<", 100)+string(rune('a'+i)))
				}
				return names, nil
			}}
			if mode == "no-capability" {
				repo = struct{ db.EntryRepo }{base}
			}
			calls := 0
			service := NewService(repo, func(string) (Provider, error) {
				return noteProviderFunc(func(_ context.Context, req Request) (Response, error) {
					calls++
					var prompt struct {
						Notes []noteContext `json:"notes"`
						Tags  tagHints      `json:"tag_context"`
					}
					if err := json.Unmarshal([]byte(req.Prompt), &prompt); err != nil {
						t.Fatal(err)
					}
					encoded, _ := json.Marshal(prompt.Tags)
					if len(encoded) > maxTagHintBytes || len(prompt.Tags.Known) > 40 || len(prompt.Notes) != 0 {
						t.Fatal("unbounded hints or broadened note context")
					}
					if mode == "bounded" && (len(prompt.Tags.Known) == 0 || prompt.Tags.Known[0] != "deployment") {
						t.Fatal(prompt.Tags)
					}
					if mode != "bounded" && len(prompt.Tags.Known) != 0 {
						t.Fatal("failed lookup leaked tags")
					}
					return Response{Text: `{"action":"create_note","title":"Deployment","body":"Cleaned","tags":["deployment"]}`}, nil
				}), nil
			}, ServiceOptions{Enabled: true})
			result, err := service.Note(ctx, NoteRequest{Text: "deployment", Namespace: "selected", Action: "create"}, "")
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) || calls != 0 || noteCount(t, base) != 0 {
					t.Fatal("lookup cancellation saved/called provider")
				}
			} else if err != nil || result.FallbackReason != "" || len(result.Entry.Tags) == 0 {
				t.Fatalf("%#v %v", result, err)
			}
		})
	}
}

func TestTagHintExplicitDefaultsBudget(t *testing.T) {
	repo := noteTestRepo(t)
	service := NewService(repo, nil, ServiceOptions{DefaultTags: []string{strings.Repeat("x", 129), " Default "}})
	request := NoteRequest{Namespace: "selected", Tags: []string{" User ", "user", " "}}
	for i := 0; i < 100; i++ {
		request.Tags = append(request.Tags, strings.Repeat("<", 100)+string(rune('a'+i)))
	}
	hints := service.tagHints(context.Background(), request)
	encoded, _ := json.Marshal(hints)
	if len(encoded) > maxTagHintBytes || len(hints.Explicit) > 64 || hints.Explicit[0] != "user" {
		t.Fatal("tag hint budget exceeded")
	}
	request.Tags = nil
	hints = service.tagHints(context.Background(), request)
	if !reflect.DeepEqual(hints.Defaults, []string{"default"}) {
		t.Fatal(hints)
	}
	request.NoteID = "target"
	if len(service.tagHints(context.Background(), request).Defaults) != 0 {
		t.Fatal("expansion defaults hinted as retained")
	}
}

func TestTagPolicyCASConflictAndCancellation(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "conflict", true: "cancellation"}[cancelRequest], func(t *testing.T) {
			repo := noteTestRepo(t)
			initial := seedNote(t, repo, "target", "default", "baseline")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			service := NewService(repo, func(string) (Provider, error) {
				return noteProviderFunc(func(_ context.Context, _ Request) (Response, error) {
					edit := initial
					edit.Version++
					edit.Tags = []string{"human"}
					edit.Body = "human edit"
					if _, err := repo.UpdateEntryCAS(context.Background(), edit, initial.Version); err != nil {
						t.Fatal(err)
					}
					if cancelRequest {
						cancel()
					}
					return Response{Text: `{"action":"expand_note","target_id":"target","title":"AI","body":"AI addition","tags":[" Deployment "]}`}, nil
				}), nil
			}, ServiceOptions{Enabled: true})
			result, err := service.Note(ctx, NoteRequest{Text: " exact incoming\n ", NoteID: "target"}, "")
			if cancelRequest {
				if !errors.Is(err, context.Canceled) || noteCount(t, repo) != 1 {
					t.Fatal("cancellation saved")
				}
			} else if err != nil || result.FallbackReason == "" || result.Entry.Body != " exact incoming\n " || len(result.Entry.Tags) != 0 || noteCount(t, repo) != 2 {
				t.Fatalf("bad raw conflict fallback: %#v %v", result, err)
			}
			target, _ := repo.GetEntry(context.Background(), "target")
			if target.Version != 2 || target.Body != "human edit" || !reflect.DeepEqual(target.Tags, []string{"human"}) {
				t.Fatal("AI overwrote concurrent tags/body")
			}
		})
	}
}

func TestTagLookupFailureDoesNotBypassFinalValidation(t *testing.T) {
	base := noteTestRepo(t)
	repo := vocabularyRepo{EntryRepo: base, lookup: func(context.Context, string, string, time.Time, int) ([]string, error) {
		return nil, errors.New("lookup unavailable")
	}}
	service := fixedNoteService(repo, `{"action":"create_note","title":"AI","body":"Cleaned","tags":[]}`, ServiceOptions{Enabled: true})
	result, err := service.Note(context.Background(), NoteRequest{Text: " exact raw "}, "")
	if err != nil || result.FallbackReason == "" || result.Entry.Body != " exact raw " || len(result.Entry.Tags) != 0 {
		t.Fatalf("%#v %v", result, err)
	}
}
