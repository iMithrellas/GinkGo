package format

import (
	"encoding/json"
	"io"

	"github.com/mithrel/ginkgo/pkg/api"
)

func WriteJSONEntries(w io.Writer, entries []api.Entry, indent bool) error {
	enc := json.NewEncoder(w)
	if indent {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(entries)
}

func WriteJSONEntry(w io.Writer, e api.Entry, indent bool) error {
	enc := json.NewEncoder(w)
	if indent {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(e)
}
