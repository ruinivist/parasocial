package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Model is the initial terminal UI for parasocial.
type Model struct {
	streamers []string
}

// New returns a Bubble Tea model that displays configured streamers.
func New(streamers []string) Model {
	return Model{streamers: append([]string(nil), streamers...)}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m Model) View() string {
	var builder strings.Builder
	builder.WriteString("Streamers\n")

	for i, streamer := range m.streamers {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, streamer)
	}

	builder.WriteString("\n")
	return builder.String()
}
