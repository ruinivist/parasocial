package tui

import (
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

var (
	pageStyle = lipgloss.NewStyle().
			Padding(1, 2)
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E6EDF3"))
	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6E7681"))
	accentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7CE38B"))
	warnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F0883E"))
	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF7B72"))
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#30363D")).
			Padding(1, 2)
	focusedPanelStyle = panelStyle.
				BorderForeground(lipgloss.Color("#7CE38B"))
	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B949E")).
			Bold(true)
	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#0D1117")).
			Background(lipgloss.Color("#7CE38B"))
	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8B949E")).
				Background(lipgloss.Color("#1B1F24"))
	statusLiveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7CE38B"))
	statusIdleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B949E"))
	statusErrorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF7B72"))
	rowNameStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#C9D1D9")).
			PaddingLeft(1)
	rowDescStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B949E")).
			PaddingLeft(1)
	selectedRowNameStyle = rowNameStyle.
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(lipgloss.Color("#7CE38B")).
				Foreground(lipgloss.Color("#E6EDF3"))
	selectedRowDescStyle = rowDescStyle.
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(lipgloss.Color("#7CE38B"))
)

func newAuthViewport(width, height int) viewport.Model {
	vp := viewport.New(width, height)
	vp.Style = lipgloss.NewStyle()
	return vp
}

func newIRCViewport(width, height int) viewport.Model {
	vp := viewport.New(width, height)
	vp.Style = lipgloss.NewStyle()
	return vp
}

func newMinerViewport(width, height int) viewport.Model {
	vp := viewport.New(width, height)
	vp.Style = lipgloss.NewStyle()
	return vp
}
