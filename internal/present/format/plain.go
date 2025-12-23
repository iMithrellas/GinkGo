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
