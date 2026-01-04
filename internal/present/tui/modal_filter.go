package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	lipglossv2 "github.com/charmbracelet/lipgloss/v2"
)

// filterModal is a foreground modal with inputs to filter the list view.
type filterModal struct {
	tagsAny   textinput.Model
	tagsAll   textinput.Model
	since     textinput.Model
	until     textinput.Model
	width     int
	height    int
	padX      int
	padY      int
	box       lipglossv2.Style
	namespace string
	focus     int
}

func newFilterModal(tagsAny, tagsAll, since, until, namespace string, termW, termH int) *filterModal {
	m := &filterModal{
		padX:      2,
		padY:      1,
		namespace: namespace,
	}
	m.tagsAny = newFilterInput("tags-any: ", "work,ginkgo", tagsAny)
	m.tagsAll = newFilterInput("tags-all: ", "work,ginkgo", tagsAll)
	m.since = newFilterInput("since: ", "2h | 2025-10-26T14:30", since)
	m.until = newFilterInput("until: ", "3d | 2025-10-26", until)
	m.setFocus(0)
	m.resizeForTerm(termW, termH)
	return m
}

func newFilterInput(prompt, placeholder, value string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Placeholder = placeholder
	ti.SetValue(value)
	return ti
}

func (m *filterModal) resizeForTerm(termW, termH int) {
	if termW <= 0 || termH <= 0 {
		termW, termH = 80, 24
	}
	w := int(float64(termW) * 0.6)
	if termW < 80 {
		w = termW - 4
	}
	if w < 46 {
		w = max(42, termW-2)
	}
	if w > 90 {
		w = 90
	}
	h := int(float64(termH) * 0.45)
	if termH < 20 {
		h = termH - 4
	}
	if h < 12 {
		h = max(10, termH-1)
	}
	if h > 22 {
		h = 22
	}
	m.width, m.height = w, h
	m.box = lipglossv2.NewStyle().
		Width(w).
		Height(h).
		Padding(m.padY, m.padX).
		Border(lipglossv2.RoundedBorder()).
		BorderForeground(lipglossv2.Color("63"))

	innerW := w - 2 - m.padX*2
	minW := 12
	if innerW < minW {
		innerW = minW
	}
	m.tagsAny.Width = max(minW, innerW-lipgloss.Width(m.tagsAny.Prompt))
	m.tagsAll.Width = max(minW, innerW-lipgloss.Width(m.tagsAll.Prompt))
	m.since.Width = max(minW, innerW-lipgloss.Width(m.since.Prompt))
	m.until.Width = max(minW, innerW-lipgloss.Width(m.until.Prompt))
}

func (m *filterModal) setFocus(idx int) {
	m.focus = idx
	inputs := []*textinput.Model{&m.tagsAny, &m.tagsAll, &m.since, &m.until}
	for i, in := range inputs {
		if i == idx {
			in.Focus()
		} else {
			in.Blur()
		}
	}
}

func (m *filterModal) values() (string, string, string, string) {
	return m.tagsAny.Value(), m.tagsAll.Value(), m.since.Value(), m.until.Value()
}

func (m *filterModal) update(msg tea.Msg) (*filterModal, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.resizeForTerm(x.Width, x.Height)
		return m, nil
	case tea.KeyMsg:
		switch x.String() {
		case "tab", "down":
			m.setFocus((m.focus + 1) % 4)
			return m, nil
		case "shift+tab", "up":
			m.setFocus((m.focus + 3) % 4)
			return m, nil
		}
	}
	var cmd tea.Cmd
	switch m.focus {
	case 0:
		m.tagsAny, cmd = m.tagsAny.Update(msg)
	case 1:
		m.tagsAll, cmd = m.tagsAll.Update(msg)
	case 2:
		m.since, cmd = m.since.Update(msg)
	case 3:
		m.until, cmd = m.until.Update(msg)
	}
	return m, cmd
}

func (m *filterModal) View() string {
	title := "Filters"
	if strings.TrimSpace(m.namespace) != "" {
		// TODO: Consider surfacing namespace in a dedicated header/footer region.
		title = fmt.Sprintf("Filters (namespace: %s)", m.namespace)
	}
	header := lipgloss.NewStyle().Bold(true).Render(title)
	help := lipgloss.NewStyle().Faint(true).Render("enter=apply • esc/ctrl+q=cancel • tab=next • ctrl+x=clear")
	body := strings.Join([]string{
		header,
		"",
		m.tagsAny.View(),
		m.tagsAll.View(),
		m.since.View(),
		m.until.View(),
		"",
		help,
	}, "\n")
	return m.box.Render(body)
}

func (m *filterModal) Init() tea.Cmd                           { return nil }
func (m *filterModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m.update(msg) }
