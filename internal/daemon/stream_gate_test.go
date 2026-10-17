package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTranscriptPrefixStrict(t *testing.T) {
	for _, tc := range []struct {
		text, phrase, body string
		matched            bool
	}{
		{"Ginkgo, TAKE a note: Keep This!", "ginkgo take a note", "Keep This!", true},
		{"note -5 degrees", "note", "-5 degrees", true},
		{"note \"verbatim\"", "note", "\"verbatim\"", true},
		{"notebook shopping", "note", "", false},
		{"note-taking background", "note", "", false},
		{"note's background", "note", "", false},
		{"please take a note shopping", "take a note", "", false},
		{"take another note shopping", "take a note", "", false},
		{"take a noteworthy thing", "take a note", "", false},
		{"note", "note", "", true},
		{"anything", "...", "", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			body, matched := prefixContent(tc.text, tc.phrase)
			require.Equal(t, tc.matched, matched)
			require.Equal(t, tc.body, body)
		})
	}
}

func TestFinalConfirmedGate(t *testing.T) {
	now := time.Unix(100, 0)
	g := streamGate{enabled: true, phrase: "take note"}
	body, original, id := g.final(now, 1, "background take note nope")
	require.Empty(t, body)
	require.Empty(t, original)
	require.Empty(t, id)
	body, original, id = g.final(now, 2, "Take note: Exact Dictation!")
	require.Equal(t, "Exact Dictation!", body)
	require.Equal(t, "Take note: Exact Dictation!", original)
	require.NotEmpty(t, id)
	body, _, _ = g.final(now.Add(time.Second), 3, "take note cooldown")
	require.Empty(t, body)
	body, _, id = g.final(now.Add(3*time.Second), 4, "take note")
	require.Empty(t, body)
	require.Empty(t, id)
	require.NotEmpty(t, g.id)
	armed := g.id
	g.observe(now.Add(4*time.Second), 5, true)
	body, original, id = g.final(now.Add(20*time.Second), 5, "  next exact\nutterance  ")
	require.Equal(t, "next exact\nutterance", body)
	require.Equal(t, "  next exact\nutterance  ", original)
	require.Equal(t, armed, id)
	require.Empty(t, g.id)
}

func TestGateTimeoutCancelAndCaptionOnly(t *testing.T) {
	now := time.Unix(100, 0)
	g := streamGate{enabled: true, phrase: "note"}
	g.final(now, 1, "note")
	armed := g.id
	require.Equal(t, armed, g.observe(now.Add(5*time.Second), 2, false))
	require.Empty(t, g.id)
	body, _, id := g.final(now.Add(6*time.Second), 2, "unselected background")
	require.Empty(t, body)
	require.Empty(t, id)
	g.final(now.Add(8*time.Second), 3, "note")
	require.NotEmpty(t, g.clear(now.Add(9*time.Second)))
	g.enabled = false
	body, _, id = g.final(now.Add(time.Hour), 4, "note never save")
	require.Empty(t, body)
	require.Empty(t, id)
	require.Empty(t, g.id)
}

func TestGateActivityDoesNotConsumeArmOrDisableTimeout(t *testing.T) {
	now := time.Unix(100, 0)
	g := streamGate{enabled: true, phrase: "note"}
	g.observe(now, 1, true)
	g.final(now, 1, "note")
	id := g.id
	g.observe(now.Add(time.Second), 2, false) // discarded 10ms pulse
	require.Zero(t, g.utterance)
	require.Equal(t, id, g.id)
	g.observe(now.Add(2*time.Second), 3, true) // qualifying future utterance
	require.EqualValues(t, 3, g.utterance)
	require.Empty(t, g.observe(now.Add(20*time.Second), 0, false), "selected note must not be clipped by initial timeout")
	body, _, selected := g.final(now.Add(21*time.Second), 3, "valid dictation")
	require.Equal(t, "valid dictation", body)
	require.Equal(t, id, selected)

	g = streamGate{enabled: true, phrase: "note"}
	g.final(now, 1, "note")
	id = g.id
	g.observe(now.Add(time.Second), 2, false)
	require.Equal(t, id, g.observe(now.Add(5*time.Second), 0, false))
	require.Empty(t, g.id)
	require.Zero(t, g.utterance)
}

func TestGateDelayedActivationExcludesPreviouslyObservedActivity(t *testing.T) {
	now := time.Unix(100, 0)
	g := streamGate{enabled: true, phrase: "note"}
	g.observe(now, 1, true)                   // trigger inference pending
	g.observe(now.Add(time.Second), 2, false) // ambient onset during inference
	g.final(now.Add(2*time.Second), 1, "note")
	id := g.id
	g.observe(now.Add(3*time.Second), 2, true) // same ambient utterance qualifies later
	require.Zero(t, g.utterance)
	body, _, selected := g.final(now.Add(3*time.Second), 2, "ambient speech")
	require.Empty(t, body)
	require.Empty(t, selected)
	g.observe(now.Add(4*time.Second), 3, false) // another discarded pulse
	g.observe(now.Add(5*time.Second), 4, true)
	body, _, selected = g.final(now.Add(8*time.Second), 4, "new dictation")
	require.Equal(t, "new dictation", body)
	require.Equal(t, id, selected)
}
