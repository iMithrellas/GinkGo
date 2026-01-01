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
		canFetchPrev: true,
		canFetchNext: true,
		pageSize:     10,
		bufferRatio:  0.2,
	}
	m.initTable()
	m.table.SetHeight(10)
	m.updateWindowSize()

	m.table.SetCursor(1)
	require.True(t, m.needsWindowRefetch())

	m.canFetchPrev = false
	require.True(t, m.needsWindowRefetch())

	m.canFetchNext = false
	require.False(t, m.needsWindowRefetch())

	m.canFetchPrev = true
	m.table.SetCursor(8)
	require.True(t, m.needsWindowRefetch())
}

func TestPagingRefetchNoMoreData(t *testing.T) {
	m := model{
		entries:      makeEntries(10),
		pageSize:     10,
		bufferRatio:  0.2,
		canFetchPrev: false,
		canFetchNext: false,
	}
	m.initTable()
	m.table.SetHeight(10)
	m.updateWindowSize()

	m.table.SetCursor(0)
	require.False(t, m.needsWindowRefetch())

	m.table.SetCursor(9)
	require.False(t, m.needsWindowRefetch())
}
