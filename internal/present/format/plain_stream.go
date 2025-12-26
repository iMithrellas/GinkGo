package format

import (
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/mithrel/ginkgo/pkg/api"
)

// PlainStreamWriter incrementally writes entries in the same plain TSV format.
type PlainStreamWriter struct {
	tw          *tabwriter.Writer
	headers     bool
	wroteHeader bool
}

// NewPlainStreamWriter creates a streaming plain writer.
func NewPlainStreamWriter(w io.Writer, headers bool) *PlainStreamWriter {
	return &PlainStreamWriter{
		tw:      tabwriter.NewWriter(w, 0, 0, 2, ' ', 0),
		headers: headers,
	}
}

// WriteEntries writes a batch of entries and flushes.
func (pw *PlainStreamWriter) WriteEntries(entries []api.Entry) error {
	if pw.headers && !pw.wroteHeader {
		_, _ = io.WriteString(pw.tw, headerLine)
		pw.wroteHeader = true
	}
	for _, e := range entries {
		createdMs := e.CreatedAt
		if createdMs.IsZero() {
			createdMs = time.Time{}
		}
		ms := createdMs.UnixNano() / int64(time.Millisecond)
		line := esc(e.ID) + "\t" + esc(e.Title) + "\t" + esc(e.Namespace) + "\t" + strconv.FormatInt(ms, 10) + "\t" + esc(joinTags(e.Tags)) + "\n"
		_, _ = io.WriteString(pw.tw, line)
	}
	return pw.tw.Flush()
}

// Close flushes remaining buffered output.
func (pw *PlainStreamWriter) Close() error {
	return pw.tw.Flush()
}
