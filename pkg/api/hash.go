package api

import (
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/zeebo/blake3"
)

// Hash returns a deterministic BLAKE3 hash of the entry content.
// Nil provenance retains the historical hash byte-for-byte.
func (e Entry) Hash() string {
	h := blake3.New()

	h.Write([]byte(e.ID))
	h.Write([]byte{0})

	h.Write([]byte(e.Title))
	h.Write([]byte{0})

	h.Write([]byte(e.Body))
	h.Write([]byte{0})

	sortedTags := append([]string(nil), e.Tags...)
	sort.Strings(sortedTags)
	for _, t := range sortedTags {
		h.Write([]byte(strings.ToLower(t)))
		h.Write([]byte{0})
	}
	h.Write([]byte{0}) // End of tags

	h.Write([]byte(e.Namespace))
	h.Write([]byte{0})

	if !e.CreatedAt.IsZero() {
		h.Write([]byte(e.CreatedAt.UTC().Format(timeRFC3339Nano)))
	}
	h.Write([]byte{0})

	if !e.UpdatedAt.IsZero() {
		h.Write([]byte(e.UpdatedAt.UTC().Format(timeRFC3339Nano)))
	}

	if e.Provenance != nil {
		// Domain separation and length prefixes keep originals unambiguous, even
		// when they contain nulls. Source order is significant; times use UTC.
		h.Write([]byte{0})
		h.Write([]byte("ginkgo:provenance:v1"))
		writeString := func(s string) {
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(len(s)))
			h.Write(size[:])
			h.Write([]byte(s))
		}
		for _, source := range e.Provenance.Sources {
			writeString(source.ID)
			writeString(source.CapturedAt.UTC().Format(timeRFC3339Nano))
			writeString(source.Kind)
			writeString(source.OriginalText)
			writeString(source.Language)
			writeString(source.STTEngine)
			writeString(source.STTModel)
		}
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)
}

const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
