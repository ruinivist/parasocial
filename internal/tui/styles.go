package tui

import (
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleColor         = lipgloss.Color("#E6EDF3")
	mutedColor         = lipgloss.Color("#8B949E")
	accentColor        = lipgloss.Color("#7CE38B")
	warnColor          = lipgloss.Color("#F0883E")
	errorColor         = lipgloss.Color("#FF7B72")
	panelBorderColor   = lipgloss.Color("#30363D")
	tabActiveTextColor = lipgloss.Color("#0D1117")
	tabInactiveBgColor = lipgloss.Color("#1B1F24")
	rowTextColor       = lipgloss.Color("#C9D1D9")
)

var (
	pageStyle = lipgloss.NewStyle().
			Padding(1, 2)
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(titleColor)
	mutedStyle = lipgloss.NewStyle().
			Foreground(mutedColor)
	accentStyle = lipgloss.NewStyle().
			Foreground(accentColor)
	warnStyle = lipgloss.NewStyle().
			Foreground(warnColor)
	errorStyle = lipgloss.NewStyle().
			Foreground(errorColor)
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(panelBorderColor).
			Padding(1, 2)
	focusedPanelStyle = panelStyle.Copy().
				BorderForeground(accentColor)
	labelStyle = mutedStyle.Copy().
			Bold(true)
	activeTabStyle = accentStyle.Copy().
			Bold(true).
			Foreground(tabActiveTextColor).
			Background(accentColor)
	inactiveTabStyle = mutedStyle.Copy().
				Background(tabInactiveBgColor)
	rowNameStyle = lipgloss.NewStyle().
			Foreground(rowTextColor).
			PaddingLeft(1)
	selectedRowNameStyle = rowNameStyle.Copy().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(accentColor).
				Foreground(titleColor)
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
