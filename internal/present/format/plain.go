package format

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mithrel/ginkgo/pkg/api"
)

// TSV columns: id, title, namespace, created_unix_ms, tags
var headerLine = "id\ttitle\tnamespace\tcreated_unix_ms\ttags\n"

func esc(field string) string {
	field = strings.ReplaceAll(field, "\t", "\\t")
	field = strings.ReplaceAll(field, "\n", "\\n")
	return field
}

func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	// Join with commas; no spaces
	var b strings.Builder
	for i, t := range tags {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(t)
	}
	return b.String()
}

func WritePlainEntries(w io.Writer, entries []api.Entry, headers bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if headers {
		_, _ = io.WriteString(tw, headerLine)
	}
	for _, e := range entries {
		createdMs := e.CreatedAt
		if createdMs.IsZero() {
			createdMs = time.Time{}
		}
		ms := createdMs.UnixNano() / int64(time.Millisecond)
		line := fmt.Sprintf("%s\t%s\t%s\t%d\t%s\n",
			esc(e.ID), esc(e.Title), esc(e.Namespace), ms, esc(joinTags(e.Tags)))
		_, _ = io.WriteString(tw, line)
	}
	return tw.Flush()
}

func WritePlainEntry(w io.Writer, e api.Entry, headers bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if headers {
		_, _ = io.WriteString(tw, headerLine)
	}
	ms := e.CreatedAt.UnixNano() / int64(time.Millisecond)
	line := fmt.Sprintf("%s\t%s\t%s\t%d\t%s\n",
		esc(e.ID), esc(e.Title), esc(e.Namespace), ms, esc(joinTags(e.Tags)))
	_, _ = io.WriteString(tw, line)
	return tw.Flush()
}

// Pretty single-entry rendering delegates to existing UI formatting for now.
// Later, wire a glamour-based renderer here.
func WritePrettyEntry(w io.Writer, e api.Entry) error {
	// Keep dependency minimal by formatting here to avoid import cycle.
	// Plain, readable layout until glamour integration.
	ts := e.CreatedAt.Local().Format(time.RFC3339)
	tags := joinTags(e.Tags)
	_, _ = fmt.Fprintf(w, "ID: %s\nCreated: %s\nTitle: %s\nTags: %s\n---\n%s\n", e.ID, ts, e.Title, tags, strings.TrimSpace(e.Body))
	return nil
}
