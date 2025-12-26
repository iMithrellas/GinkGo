package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/pkg/api"
)

func makeEntries(n int) []api.Entry {
	now := time.Now().UTC().Truncate(time.Second)
	out := make([]api.Entry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, api.Entry{
			ID:        string(rune('a' + i)),
			Title:     "t",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	return out
}

func TestPagingTriggers(t *testing.T) {
	m := model{
		entries:      makeEntries(10),
		nextCursor:   "next",
		prevCursor:   "prev",
		canFetchPrev: true,
		pageSize:     10,
		bufferSize:   2,
		bufferRatio:  0.2,
	}
	m.initTable()

	m.table.SetCursor(8)
	cmd := m.maybeFetchNext()
	require.NotNil(t, cmd)
	require.True(t, m.loadingNext)

	m.loadingNext = false
	m.table.SetCursor(1)
	cmd = m.maybeFetchPrev()
	require.NotNil(t, cmd)
	require.True(t, m.loadingPrev)
}

func TestPagingPrune(t *testing.T) {
	m := model{
		entries:      makeEntries(10),
		pageSize:     10,
		bufferSize:   2,
		bufferRatio:  0.2,
		canFetchPrev: true,
	}
	m.initTable()
	m.table.SetHeight(1)
	m.table.SetCursor(6)

	m.maybePrune()
	require.Len(t, m.entries, 6)
	require.Equal(t, 2, m.table.Cursor())
}
