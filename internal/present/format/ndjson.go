package format

import (
	"encoding/json"
	"io"

	"github.com/mithrel/ginkgo/pkg/api"
)

// WriteNDJSONEntries writes entries as newline-delimited JSON objects.
func WriteNDJSONEntries(w io.Writer, entries []api.Entry) error {
	enc := json.NewEncoder(w)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// WriteNDJSONEntry writes a single entry as one JSON line.
func WriteNDJSONEntry(w io.Writer, e api.Entry) error {
	enc := json.NewEncoder(w)
	return enc.Encode(e)
}
