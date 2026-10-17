package sherpavoice

import (
	"strings"
	"unicode"
)

// selectedBody is a veto on already acoustically selected source audio, NEVER
// wake authority. Only complete leading trusted phrases count. Repetitions and
// punctuation alone are control-only. Original and wire Body remain untouched.
func selectedBody(original, phrase string) (string, bool) {
	text := strings.TrimSpace(original)
	matched := false
	for {
		body, ok := stripWholeLeading(text, phrase)
		if !ok && strings.EqualFold(phrase, "Hey Ginkgo") {
			// The pinned Parakeet v2 FP32 emits this conventional alternate
			// spelling on actual selected fixture audio. No fuzzy phonetics,
			// aliases, missing Hey, or arbitrary ASR-only activation are allowed.
			body, ok = stripWholeLeading(text, "Hey Ginko")
		}
		if !ok {
			return text, matched
		}
		matched = true
		text = body
	}
}

func stripWholeLeading(text, phrase string) (string, bool) {
	// Surrounding punctuation is harmless; punctuation inside words is not.
	text = strings.TrimLeftFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
	if phrase == "" || len(text) < len(phrase) || !strings.EqualFold(text[:len(phrase)], phrase) {
		return text, false
	}
	rest := text[len(phrase):]
	if rest != "" {
		r := []rune(rest)[0]
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) {
			return text, false
		}
	}
	return strings.TrimLeftFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }), true
}
