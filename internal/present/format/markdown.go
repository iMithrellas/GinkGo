package format

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/mithrel/ginkgo/pkg/api"
)

// WritePrettyEntry renders a single entry with markdown formatting using glamour.
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
