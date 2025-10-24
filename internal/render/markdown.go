package render

import "strings"

// Markdown renders a minimal plaintext representation.
// Wireframe: replace with glamour-based renderer later.
func Markdown(s string) string {
    // Very naive: collapse multiple blank lines and trim.
    s = strings.ReplaceAll(s, "\r\n", "\n")
    return strings.TrimSpace(s)
}

