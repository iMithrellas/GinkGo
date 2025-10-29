package format

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/glamour"
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

// Pretty single-entry rendering with markdown formatting using glamour.
func WritePrettyEntry(w io.Writer, e api.Entry) error {
	ts := e.CreatedAt.Local().Format(time.RFC3339)
	tags := joinTags(e.Tags)

	md := fmt.Sprintf(`# %s

> **ID:** %s | **Created:** %s
>
> **Tags:** %s

---

%s
`, e.Title, e.ID, ts, tags, strings.TrimSpace(e.Body))

	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dracula"),
		glamour.WithWordWrap(80),
	)
	if err != nil {
		return fmt.Errorf("failed to create renderer: %w", err)
	}

	out, err := r.Render(md)
	if err != nil {
		return fmt.Errorf("failed to render markdown: %w", err)
	}

	_, err = io.WriteString(w, out)
	return err
}
