package sherpavoice

import (
	"testing"
)

func TestPinnedKWSResetClockIsNotAnEpochClock(t *testing.T) {
	for _, kind := range []string{"manual_detection_reset", "automatic_blank_reset"} {
		t.Run(kind, func(t *testing.T) {
			// Source-model regression, NOT native execution. InitOnlineStream resets
			// decoder frame offsets, but OnlineStream retains duplicate suppression.
			// Both Reset paths call it; the C result exposes no reset-generation origin.
			lastTimestamp := 10.0
			resetEpoch := 15.0
			local := 5.0
			accepted := local > lastTimestamp
			if accepted {
				t.Fatal("pinned stream should suppress the post-reset timestamp")
			}
			local = 12
			if local <= lastTimestamp {
				t.Fatal("expected later accepted local timestamp")
			}
			actualEpoch := resetEpoch + local
			reportedEpoch := local // start_time has not acquired the missing reset origin
			if reportedEpoch >= actualEpoch {
				t.Fatal("source regression no longer demonstrates unsafe attribution")
			}
		})
	}
}

func TestAliasCanonicalFormAndUnknownForm(t *testing.T) {
	for _, test := range []struct {
		name, spoken, text string
		want               int
	}{
		{"known_alias_trigger_only", "hey ginkgo", "hey ginkgo", 0},
		{"known_alias_dictation", "hey ginkgo", "hey ginkgo exact dictation", 1},
		{"unknown_alias_trigger_only", "", "hey ginkgo", 0},
		{"unknown_alias_dictation", "", "some dictation", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "NOTE_ALIAS", SpokenForm: test.spoken, Span: Span{0, 8000}}}, activity: Activity{Segments: []Span{{0, 8000}}}}}}
			e, events := newTestEngine(t, f, &fakeASR{text: test.text})
			feed(t, e, 1)
			if err := e.Stop(); err != nil {
				t.Fatal(err)
			}
			if got := count(*events, "note_candidate"); got != test.want {
				t.Fatalf("candidates=%d events=%+v", got, *events)
			}
		})
	}
}
