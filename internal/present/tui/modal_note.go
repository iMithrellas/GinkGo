package tui

import (
	"bytes"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	lipglossv2 "github.com/charmbracelet/lipgloss/v2"

	"github.com/mithrel/ginkgo/internal/present/format"
	"github.com/mithrel/ginkgo/pkg/api"
)

// noteModal is a foreground modal showing the full rendered note
// using Glamour inside a scrollable viewport.
type noteModal struct {
	e       api.Entry
	vp      viewport.Model
	width   int
	height  int
	padX    int
	padY    int
	box     lipglossv2.Style
	content string
}

func newNoteModal(e api.Entry, termW, termH int) *noteModal {
	m := &noteModal{e: e, padX: 2, padY: 1}
	m.resizeForTerm(termW, termH)
	m.setContent("Loading…")
	return m
}

func (m *noteModal) resizeForTerm(termW, termH int) {
	if termW <= 0 || termH <= 0 {
		termW, termH = 80, 24
	}
	// 60% width, or nearly full width if terminal is small (<80 cols)
	w := int(float64(termW) * 0.6)
	if termW < 80 {
		w = termW - 4
	}
	if w < 40 {
		w = max(32, termW-2)
	}
	h := int(float64(termH) * 0.7)
	if termH < 20 {
		h = termH - 2
	}
	if h < 10 {
		h = max(8, termH-1)
	}
	m.width, m.height = w, h
	m.box = lipglossv2.NewStyle().
		Width(w).
		Height(h).
		Padding(m.padY, m.padX).
		Border(lipglossv2.RoundedBorder()).
		BorderForeground(lipglossv2.Color("63"))

	innerW := w - 2 - m.padX*2 // borders + padding
	innerH := h - 2 - m.padY*2
	if innerW < 10 {
		innerW = 10
	}
	if innerH < 5 {
		innerH = 5
	}
	if m.vp.Width == 0 {
		m.vp = viewport.New(innerW, innerH)
	} else {
		m.vp.Width = innerW
		m.vp.Height = innerH
	}
	// Re-apply current content with new size
	m.vp.SetContent(m.content)
}

// setEntry renders the entry using the shared pretty renderer and sets it.
func (m *noteModal) setEntry(e api.Entry) {
	m.e = e
	var buf bytes.Buffer

	_ = format.WritePrettyEntry(&buf, e)
	m.setContent(buf.String())
}

func (m *noteModal) setContent(s string) {
	m.content = s
	m.vp.SetContent(s)
}

func (m *noteModal) update(msg tea.Msg) (*noteModal, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.resizeForTerm(x.Width, x.Height)
		return m, nil
	case tea.KeyMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m *noteModal) Init() tea.Cmd                           { return nil }
func (m *noteModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m.update(msg) }
func (m *noteModal) View() string                            { return m.box.Render(m.vp.View()) }
