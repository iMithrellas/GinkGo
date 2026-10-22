package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/notes"
	"github.com/mithrel/ginkgo/pkg/api"
)

type providerFunc func(context.Context, ai.Request) (ai.Response, error)

func (f providerFunc) Complete(ctx context.Context, r ai.Request) (ai.Response, error) {
	return f(ctx, r)
}

func setup(t *testing.T, responses ...string) (*Service, *db.Store, *notes.Service) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "notes.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	index := 0
	model := providerFunc(func(ctx context.Context, request ai.Request) (ai.Response, error) {
		if index >= len(responses) {
			return ai.Response{}, errors.New("unexpected generation")
		}
		text := responses[index]
		index++
		return ai.Response{Text: text}, nil
	})
	generator := ai.NewService(store.Entries, func(string) (ai.Provider, error) { return model, nil }, ai.ServiceOptions{Enabled: true})
	return NewService(store.Entries, generator), store, notes.NewService(store.Entries)
}

func plan(action string, targets ...string) string {
	if targets == nil {
		targets = []string{}
	}
	data, _ := json.Marshal(intent{Action: action, Targets: targets})
	return string(data)
}

func TestMeetingAppendPreservesOriginal(t *testing.T) {
	s, store, writer := setup(t)
	ctx := context.Background()
	initial, err := writer.Create(ctx, notes.CreateRequest{Namespace: "default", Title: "Topics for the move-up meeting", Body: "Discuss Vector and Terraform.", OriginalText: "meeting agenda", Tags: []string{"meeting"}})
	require.NoError(t, err)
	responses := []string{plan("append", initial.ID), `{"action":"expand_note","target_id":"` + initial.ID + `","title":"Topics for the move-up meeting","body":"Discovery options: script, VMagent, or Vector.","tags":["monitoring"]}`}
	index := 0
	s.ai = ai.NewService(store.Entries, func(string) (ai.Provider, error) {
		return providerFunc(func(_ context.Context, r ai.Request) (ai.Response, error) {
			if index == 0 {
				require.Contains(t, r.Prompt, initial.ID)
				require.Contains(t, r.Prompt, "created_at")
			}
			text := responses[index]
			index++
			return ai.Response{Text: text}, nil
		}), nil
	}, ai.ServiceOptions{Enabled: true})
	input := "Expand the last note regarding the move-up meeting with discovery options."
	result, err := s.Turn(ctx, "voice", ai.NoteRequest{Text: input, Namespace: "default", Source: &api.Source{Kind: "voice"}}, "")
	require.NoError(t, err)
	require.NotNil(t, result.Saved)
	require.Equal(t, initial.ID, result.Saved.Entry.ID)
	require.Contains(t, result.Saved.Entry.Body, initial.Body)
	require.EqualValues(t, 2, result.Saved.Entry.Version)
	require.Equal(t, input, result.Saved.Entry.Provenance.Sources[1].OriginalText)
	entries, _, err := store.Entries.ListEntries(ctx, api.ListQuery{Namespace: "default"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestSearchReadSummaryAndIsolation(t *testing.T) {
	s, store, writer := setup(t)
	ctx := context.Background()
	a, err := writer.Create(ctx, notes.CreateRequest{Namespace: "work", Title: "Nomad", Body: "Use discovery", Tags: []string{"nomad"}})
	require.NoError(t, err)
	responses := []string{`{"action":"search","query":"Nomad","targets":[],"question":""}`, plan("search"), plan("read", a.ID), plan("summarize", a.ID), "Discovery summary", plan("read", a.ID)}
	i := 0
	s.ai = ai.NewService(store.Entries, func(string) (ai.Provider, error) {
		return providerFunc(func(_ context.Context, r ai.Request) (ai.Response, error) {
			if i == 2 {
				require.Contains(t, r.Prompt, `"results":[{"id":"`+a.ID)
			}
			if i == 3 {
				require.Contains(t, r.Prompt, `"current":"`+a.ID)
			}
			if i == 5 {
				require.NotContains(t, r.Prompt, a.ID)
			}
			text := responses[i]
			i++
			return ai.Response{Text: text}, nil
		}), nil
	}, ai.ServiceOptions{Enabled: true})
	for _, input := range []string{"Find Nomad notes", "Read the first one", "Summarize that"} {
		result, err := s.Turn(ctx, "cli", ai.NoteRequest{Text: input, Namespace: "work"}, "")
		require.NoError(t, err)
		require.Nil(t, result.Saved)
		require.NotEmpty(t, result.Text)
	}
	_, err = s.Turn(ctx, "cli", ai.NoteRequest{Text: "Read that", Namespace: "private"}, "")
	require.ErrorContains(t, err, "unavailable")
	entries, _, err := store.Entries.ListEntries(ctx, api.ListQuery{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestClarificationAndFailureNeverCreate(t *testing.T) {
	for _, response := range []string{`{"action":"clarify","query":"","targets":[],"question":"Which meeting?"}`, `{"action":"read","action":"create","query":"","targets":[],"question":""}`, plan("append", "invented")} {
		t.Run(response, func(t *testing.T) {
			s, store, _ := setup(t, response)
			result, err := s.Turn(context.Background(), "voice", ai.NoteRequest{Text: "Append to the meeting note"}, "")
			if result.Action == "clarify" {
				require.NoError(t, err)
				require.Equal(t, "Which meeting?", result.Text)
			} else {
				require.Error(t, err)
			}
			entries, _, err := store.Entries.ListEntries(context.Background(), api.ListQuery{})
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestClarificationFollowUpAndStrictFailure(t *testing.T) {
	s, store, writer := setup(t)
	ctx := context.Background()
	a, err := writer.Create(ctx, notes.CreateRequest{Namespace: "work", Title: "Meeting", Body: "Agenda", Tags: []string{"meeting"}})
	require.NoError(t, err)
	i := 0
	s.ai = ai.NewService(store.Entries, func(string) (ai.Provider, error) {
		return providerFunc(func(_ context.Context, r ai.Request) (ai.Response, error) {
			i++
			switch i {
			case 1:
				return ai.Response{Text: `{"action":"clarify","query":"","targets":[],"question":"Which meeting?"}`}, nil
			case 2:
				require.Contains(t, r.Prompt, "pending_input")
				require.Contains(t, r.Prompt, "Add metrics")
				return ai.Response{Text: plan("append", a.ID)}, nil
			default:
				return ai.Response{}, errors.New("provider failed")
			}
		}), nil
	}, ai.ServiceOptions{Enabled: true})
	_, err = s.Turn(ctx, "voice", ai.NoteRequest{Text: "Add metrics", Namespace: "work"}, "")
	require.NoError(t, err)
	_, err = s.Turn(ctx, "voice", ai.NoteRequest{Text: "The meeting", Namespace: "work"}, "")
	require.Error(t, err)
	entries, _, err := store.Entries.ListEntries(ctx, api.ListQuery{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.EqualValues(t, 1, entries[0].Version)
}
