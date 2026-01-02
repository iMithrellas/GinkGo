package format

import (
	"encoding/json"
	"io"

	"github.com/mithrel/ginkgo/pkg/api"
)

// NDJSONStreamWriter incrementally writes entries as NDJSON.
type NDJSONStreamWriter struct {
	enc *json.Encoder
}

// NewNDJSONStreamWriter creates a streaming NDJSON writer.
func NewNDJSONStreamWriter(w io.Writer) *NDJSONStreamWriter {
	return &NDJSONStreamWriter{enc: json.NewEncoder(w)}
}

// WriteEntries writes a batch of entries.
func (nw *NDJSONStreamWriter) WriteEntries(entries []api.Entry) error {
	for _, e := range entries {
		if err := nw.enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// Close is a no-op for NDJSON output.
func (nw *NDJSONStreamWriter) Close() error { return nil }
