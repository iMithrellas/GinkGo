package daemon

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mithrel/ginkgo/pkg/api"
)

// prefixContent matches complete leading words only. Punctuation between words
// is tolerated, but symbols/numbers are not silently removed. The returned body
// is an exact suffix of input; original input is always retained separately.
func prefixContent(input, phrase string) (string, bool) {
	words := strings.FieldsFunc(phrase, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
	if len(words) == 0 {
		return "", false
	}
	rest := input
	for index, word := range words {
		rest = strings.TrimLeftFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
		end := 0
		for end < len(rest) {
			r, n := utf8.DecodeRuneInString(rest[end:])
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				break
			}
			end += n
		}
		if !strings.EqualFold(rest[:end], word) {
			return "", false
		}
		rest = rest[end:]
		// Hyphen/apostrophe compounds are not whole-word boundaries ("note-taking"
		// must not activate "note"). Keep a deliberately small delimiter set.
		if index == len(words)-1 && rest != "" {
			r, _ := utf8.DecodeRuneInString(rest)
			if !unicode.IsSpace(r) && !strings.ContainsRune(",.:;!?", r) {
				return "", false
			}
		}
	}
	return strings.TrimLeftFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(",.:;!?", r) }), true
}

// streamGate is final-confirmed transcript-prefix activation, NOT acoustic wake
// detection. Its caller serializes access. Partial text never authorizes saving;
// qualifying capture can bind an already final-confirmed arm to an utterance.
type streamGate struct {
	phrase              string
	enabled             bool
	waitUntil, cooldown time.Time
	id                  string
	utterance           uint64
	observed, armAfter  uint64
}

// observe tracks every capture-local utterance, including unqualified activity,
// but only binds qualifying partial/final audio. A discarded sub-minimum pulse
// must neither consume the arm nor disable its initial wait timeout. The onset
// watermark also excludes ambient utterances observed before final activation.
func (g *streamGate) observe(now time.Time, utterance uint64, qualified bool) string {
	g.observed = max(g.observed, utterance)
	if g.id != "" && g.utterance == 0 && !now.Before(g.waitUntil) {
		return g.clear(now)
	}
	if g.id != "" && g.utterance == 0 && qualified && utterance > g.armAfter {
		g.utterance = utterance
	}
	return ""
}

func (g *streamGate) clear(now time.Time) string {
	id := g.id
	g.id, g.utterance = "", 0
	g.waitUntil = time.Time{}
	if id != "" {
		g.cooldown = now.Add(2 * time.Second)
	}
	return id
}

// final returns selected content, its exact final original, and session.
// A phrase alone waits at most five seconds for one future qualifying utterance.
func (g *streamGate) final(now time.Time, utterance uint64, text string) (body, original, id string) {
	if !g.enabled {
		return
	}
	if g.id != "" && g.utterance == utterance {
		id, original = g.id, text
		body = strings.TrimSpace(text)
		if selected, matched := prefixContent(text, g.phrase); matched {
			body = strings.TrimSpace(selected)
		}
		g.clear(now)
		return
	}
	if g.id != "" || now.Before(g.cooldown) {
		return
	}
	selected, matched := prefixContent(text, g.phrase)
	if !matched {
		return
	}
	id = api.NewID()
	body, original = strings.TrimSpace(selected), text
	if body == "" {
		g.id, g.waitUntil = id, now.Add(5*time.Second)
		g.armAfter = max(utterance, g.observed)
		return "", "", ""
	}
	g.cooldown = now.Add(2 * time.Second)
	return
}
