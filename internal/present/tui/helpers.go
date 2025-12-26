package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/pkg/api"
)

func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	s := tags[0]
	for i := 1; i < len(tags); i++ {
		s += ", " + tags[i]
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func encodeCursor(e api.Entry) string {
	ts := e.CreatedAt.UTC().Format(time.RFC3339Nano)
	return fmt.Sprintf("%s|%s", ts, e.ID)
}
