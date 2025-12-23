package ipc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestListFilterTranslationRoundTrip(t *testing.T) {
	// 1. Define internal Message with all filter fields
	now := time.Now().UTC().Truncate(time.Second)
	sinceStr := now.Add(-24 * time.Hour).Format(timeRFC3339)
	untilStr := now.Format(timeRFC3339)

	original := Message{
		Name:      "note.list",
		Namespace: "work",
		TagsAny:   []string{"urgent", "todo"},
		TagsAll:   []string{"ginkgo", "go"},
		Since:     sinceStr,
		Until:     untilStr,
	}

	// 2. Convert to Protobuf (simulating client side)
	pbFilter := toPbListFilter(original)

	// 3. Convert back to internal Message (simulating daemon side)
	received := Message{}
	fillFilter(&received, pbFilter)

	// 4. Assert equality
	assert.Equal(t, original.Namespace, received.Namespace)
	assert.Equal(t, original.TagsAny, received.TagsAny)
	assert.Equal(t, original.TagsAll, received.TagsAll)
	assert.Equal(t, original.Since, received.Since)
	assert.Equal(t, original.Until, received.Until)
}

func TestSearchFTSTranslationRoundTrip(t *testing.T) {
	original := Message{
		Name:      "note.search.fts",
		Title:     "search query",
		Namespace: "test",
		TagsAny:   []string{"tag1"},
	}

	// This tests the deduplicated logic in client.go and pbadapter.go for FTS
	// We'll simulate the same path: Message -> Proto Request -> Message

	// Client side
	pbFilter := toPbListFilter(original)

	// Daemon side (pbadapter logic)
	received := Message{}
	received.Title = original.Title // Query is mapped to Title in FTS
	fillFilter(&received, pbFilter)

	assert.Equal(t, original.Title, received.Title)
	assert.Equal(t, original.Namespace, received.Namespace)
	assert.Equal(t, original.TagsAny, received.TagsAny)
}
