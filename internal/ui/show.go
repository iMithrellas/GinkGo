package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/internal/render"
	"github.com/mithrel/ginkgo/pkg/api"
)

// FormatEntry returns a human-readable detail view of a note,
// matching the `note show` output.
func FormatEntry(e api.Entry) string {
	tags := ""
	if len(e.Tags) > 0 {
		tags = strings.Join(e.Tags, ", ")
	}
	return fmt.Sprintf(
		"ID: %s\nCreated: %s\nTitle: %s\nTags: %s\n---\n%s\n",
		e.ID,
		e.CreatedAt.Local().Format(time.RFC3339),
		e.Title,
		tags,
		render.Markdown(e.Body),
	)
}
