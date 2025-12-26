package util

import "github.com/sahilm/fuzzy"

// ScoreCompletions returns the top N matches for the input string from the candidates list.
// TODO: Currently uses fuzzy matching; will incorporate frecency scoring in the future.
func ScoreCompletions(input string, candidates []string, n int) []string {
	if input == "" {
		return candidates
	}
	matches := fuzzy.Find(input, candidates)
	if len(matches) == 0 {
		return nil
	}

	limit := n
	if n <= 0 || len(matches) < limit {
		limit = len(matches)
	}

	out := make([]string, limit)
	for i := 0; i < limit; i++ {
		out[i] = matches[i].Str
	}
	return out
}
