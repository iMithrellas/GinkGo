package api

import (
	"encoding/hex"
	"sort"
	"strings"

	"github.com/zeebo/blake3"
)

// Hash returns a deterministic BLAKE3 hash of the entry content.
// It includes ID, Title, Body, Tags (sorted), Namespace, and Timestamps.
func (e Entry) Hash() string {
	h := blake3.New()

	// Use null bytes or similar delimiters to prevent boundary attacks
	// (though length-prefixing or structured writing is safer, this is simple)

	h.Write([]byte(e.ID))
	h.Write([]byte{0})

	h.Write([]byte(e.Title))
	h.Write([]byte{0})

	h.Write([]byte(e.Body))
	h.Write([]byte{0})

	// Sort tags for determinism
	sortedTags := append([]string(nil), e.Tags...)
	sort.Strings(sortedTags)
	for _, t := range sortedTags {
		h.Write([]byte(strings.ToLower(t)))
		h.Write([]byte{0})
	}
	h.Write([]byte{0}) // End of tags

	h.Write([]byte(e.Namespace))
	h.Write([]byte{0})

	// Timestamps in RFC3339Nano (UTC)
	if !e.CreatedAt.IsZero() {
		h.Write([]byte(e.CreatedAt.UTC().Format(timeRFC3339Nano)))
	}
	h.Write([]byte{0})

	if !e.UpdatedAt.IsZero() {
		h.Write([]byte(e.UpdatedAt.UTC().Format(timeRFC3339Nano)))
	}

	sum := h.Sum(nil)
	return hex.EncodeToString(sum)
}

const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
