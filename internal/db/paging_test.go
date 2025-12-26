package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/pkg/api"
	"github.com/stretchr/testify/require"
)

func seedEntries(t *testing.T, ctx context.Context, store *Store, count int) []api.Entry {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	out := make([]api.Entry, 0, count)
	for i := 0; i < count; i++ {
		ts := now.Add(time.Duration(-i) * time.Minute)
		e := api.Entry{
			ID:        fmt.Sprintf("note-%02d", i),
			Version:   1,
			Title:     fmt.Sprintf("title-%02d", i),
			Body:      "body",
			Tags:      []string{"tag"},
			Namespace: "test",
			CreatedAt: ts,
			UpdatedAt: ts,
		}
		created, err := store.Entries.CreateEntry(ctx, e)
		require.NoError(t, err)
		out = append(out, created)
	}
	return out
}

func TestListEntriesPaging(t *testing.T) {
	store, ctx, _ := setupTestDB(t)
	seedEntries(t, ctx, store, 5)

	first, page, err := store.Entries.ListEntries(ctx, api.ListQuery{
		Namespace: "test",
		Limit:     2,
	})
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.NotEmpty(t, page.Next)
	require.Empty(t, page.Prev)
	require.Equal(t, "note-00", first[0].ID)
	require.Equal(t, "note-01", first[1].ID)

	second, page2, err := store.Entries.ListEntries(ctx, api.ListQuery{
		Namespace: "test",
		Limit:     2,
		Cursor:    page.Next,
	})
	require.NoError(t, err)
	require.Len(t, second, 2)
	require.NotEmpty(t, page2.Next)
	require.NotEmpty(t, page2.Prev)
	require.Equal(t, "note-02", second[0].ID)
	require.Equal(t, "note-03", second[1].ID)

	third, page3, err := store.Entries.ListEntries(ctx, api.ListQuery{
		Namespace: "test",
		Limit:     2,
		Cursor:    page2.Next,
	})
	require.NoError(t, err)
	require.Len(t, third, 1)
	require.Empty(t, page3.Next)
	require.NotEmpty(t, page3.Prev)
	require.Equal(t, "note-04", third[0].ID)

	reverse, page4, err := store.Entries.ListEntries(ctx, api.ListQuery{
		Namespace: "test",
		Limit:     2,
		Cursor:    page2.Prev,
		Reverse:   true,
	})
	require.NoError(t, err)
	require.Len(t, reverse, 2)
	require.Empty(t, page4.Prev)
	require.NotEmpty(t, page4.Next)
	require.Equal(t, "note-00", reverse[0].ID)
	require.Equal(t, "note-01", reverse[1].ID)
}

func TestSearchPaging(t *testing.T) {
	store, ctx, _ := setupTestDB(t)
	seedEntries(t, ctx, store, 4)

	first, page, err := store.Entries.Search(ctx, api.SearchQuery{
		Namespace: "test",
		Query:     "title",
		Regex:     false,
		Limit:     2,
	})
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.NotEmpty(t, page.Next)
	require.Empty(t, page.Prev)

	second, page2, err := store.Entries.Search(ctx, api.SearchQuery{
		Namespace: "test",
		Query:     "title",
		Regex:     false,
		Limit:     2,
		Cursor:    page.Next,
	})
	require.NoError(t, err)
	require.Len(t, second, 2)
	require.Empty(t, page2.Next)
	require.NotEmpty(t, page2.Prev)
	require.Equal(t, "note-02", second[0].ID)
	require.Equal(t, "note-03", second[1].ID)
}
