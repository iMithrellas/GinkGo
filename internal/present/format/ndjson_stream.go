package format

import (
	"encoding/json"
	"io"

	"github.com/mithrel/ginkgo/pkg/api"
)

// NDJSONStreamWriter incrementally writes entries as NDJSON.
type NDJSONStreamWriter struct {
	w io.Writer
}

// NewNDJSONStreamWriter creates a streaming NDJSON writer.
func NewNDJSONStreamWriter(w io.Writer) *NDJSONStreamWriter {
	return &NDJSONStreamWriter{w: w}
}

// WriteEntries writes a batch of entries.
func (nw *NDJSONStreamWriter) WriteEntries(entries []api.Entry) error {
	enc := json.NewEncoder(nw.w)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// Close finishes the stream (no-op).
func (nw *NDJSONStreamWriter) Close() error { return nil }
