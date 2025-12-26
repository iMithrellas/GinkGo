package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderOverlay composes a centered modal on top of the given base view string.
func (m model) renderOverlay(base, fg string, overlayW, overlayH int) string {
	// Compute terminal size with fallbacks.
	termW, termH := m.width, m.height
	if termW <= 0 {
		termW = 80
	}
	if termH <= 0 {
		termH = 24
	}
	// Center target position.
	x := (termW - overlayW) / 2
	y := (termH - overlayH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	// Whole-view dim of the background
	dimBase := lipgloss.NewStyle().Faint(true).Render(base)
	return overlayAt(dimBase, fg, x, y, termW, termH)
}

// overlayAt overlays fg onto base at x,y within a terminal area of w,h.
// It naively splices bytes; for our ASCII-heavy UI this is acceptable.
func overlayAt(base, fg string, x, y, w, h int) string {
	// Ensure base has at least h lines and each line padded to width w.
	baseLines := strings.Split(base, "\n")
	if len(baseLines) < h {
		pad := make([]string, h-len(baseLines))
		for i := range pad {
			pad[i] = ""
		}
		baseLines = append(baseLines, pad...)
	}
	for i := range baseLines {
		if lipgloss.Width(baseLines[i]) < w {
			baseLines[i] += strings.Repeat(" ", w-lipgloss.Width(baseLines[i]))
		}
	}
	// Apply overlay line by line.
	fgLines := strings.Split(fg, "\n")
	for i, line := range fgLines {
		row := y + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		// Pad left if needed
		if lipgloss.Width(baseLines[row]) < x {
			baseLines[row] += strings.Repeat(" ", x-lipgloss.Width(baseLines[row]))
		}
		bl := baseLines[row]
		if x > len(bl) {
			bl = bl + strings.Repeat(" ", x-len(bl))
		}
		left := bl
		if x < len(bl) {
			left = bl[:x]
		}
		rightStart := x + len(line)
		right := ""
		if rightStart < len(bl) {
			right = bl[rightStart:]
		}
		baseLines[row] = left + line + right
	}
	return strings.Join(baseLines, "\n")
}
