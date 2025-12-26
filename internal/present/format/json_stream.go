package format

import (
	"encoding/json"
	"io"

	"github.com/mithrel/ginkgo/pkg/api"
)

// JSONStreamWriter incrementally writes entries as a JSON array.
type JSONStreamWriter struct {
	w        io.Writer
	indent   bool
	wroteAny bool
}

// NewJSONStreamWriter creates a streaming JSON writer.
func NewJSONStreamWriter(w io.Writer, indent bool) *JSONStreamWriter {
	return &JSONStreamWriter{w: w, indent: indent}
}

// WriteEntries writes a batch of entries.
func (jw *JSONStreamWriter) WriteEntries(entries []api.Entry) error {
	for _, e := range entries {
		var (
			b   []byte
			err error
		)
		if jw.indent {
			b, err = json.MarshalIndent(e, "  ", "  ")
		} else {
			b, err = json.Marshal(e)
		}
		if err != nil {
			return err
		}
		if !jw.wroteAny {
			if _, err := io.WriteString(jw.w, "["); err != nil {
				return err
			}
			if jw.indent {
				if _, err := io.WriteString(jw.w, "\n"); err != nil {
					return err
				}
			}
		} else {
			sep := ","
			if jw.indent {
				sep = ",\n"
			}
			if _, err := io.WriteString(jw.w, sep); err != nil {
				return err
			}
		}
		if _, err := jw.w.Write(b); err != nil {
			return err
		}
		jw.wroteAny = true
	}
	return nil
}

// Close finishes the JSON array.
func (jw *JSONStreamWriter) Close() error {
	if !jw.wroteAny {
		_, err := io.WriteString(jw.w, "[]\n")
		return err
	}
	if jw.indent {
		_, err := io.WriteString(jw.w, "\n]\n")
		return err
	}
	_, err := io.WriteString(jw.w, "]\n")
	return err
}
