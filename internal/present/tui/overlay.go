package tui

import (
	"github.com/charmbracelet/lipgloss/v2"
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

	baseLayer := lipgloss.NewLayer(dimBase).
		Width(termW).
		Height(termH)
	fgLayer := lipgloss.NewLayer(fg).
		Width(overlayW).
		Height(overlayH).
		X(x).
		Y(y)

	return lipgloss.NewCanvas(baseLayer, fgLayer).Render()
}
