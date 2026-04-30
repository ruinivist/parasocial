package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"parasocial/internal/twitch"
)

const (
	dashboardPanelGap       = 1
	detailPanelHeaderHeight = 2
)

// View renders either the login log screen or the authenticated streamer dashboard.
func (m Model) View() string {
	if m.mode == authView {
		return m.renderAuthView()
	}
	return m.renderStreamerView()
}

func (m Model) renderAuthView() string {
	body := []string{
		titleStyle.Render("Twitch Login"),
		mutedStyle.Render("Complete the device authorization flow to continue."),
		"",
		panelStyle.Width(contentWidth(m.width)).Render(m.authViewport.View()),
	}
	if m.authErr != nil {
		body = append(body, "", errorStyle.Render("Login did not complete. Press q to quit."))
	}
	return pageStyle.Render(strings.Join(body, "\n")) + "\n"
}

func (m Model) renderStreamerView() string {
	header := m.renderDashboardHeader()
	leftStyle := panelStyle
	rightStyle := panelStyle
	if m.isStreamersFocused() {
		leftStyle = focusedPanelStyle
	}
	if m.isRightPanelFocused() {
		rightStyle = focusedPanelStyle
	}

	leftContent := labelStyle.Render("Streamers") + "\n" + m.renderStreamerRows(streamerListHeight(m.height))
	left := leftStyle.
		Width(streamerListWidth(m.width)).
		Height(panelHeight(m.height)).
		Render(leftContent)
	rightContent := m.renderDetailPanel()
	right := rightStyle.
		Width(detailWidth(m.width)).
		Height(panelHeight(m.height)).
		Render(rightContent)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", dashboardPanelGap), right)
	return dashboardPageStyle.Render(header+"\n\n"+body) + "\n"
}

func (m Model) renderDashboardHeader() string {
	var identity string
	switch {
	case m.viewer != nil:
		identity = fmt.Sprintf("Logged in as %s (%s)", m.viewer.Login, m.viewer.ID)
	case m.authState != nil && m.authState.Login != "":
		identity = fmt.Sprintf("Logged in as %s", m.authState.Login)
	default:
		identity = "Logged in"
	}

	summary := "Watching: " + watchingSummary(m.streamers)
	if m.resolveErr != nil {
		summary += "  " + warnStyle.Render(fmt.Sprintf("Viewer lookup failed: %v", m.resolveErr))
	} else if m.viewer == nil {
		summary += "  " + mutedStyle.Render("Resolving viewer identity...")
	}
	return titleStyle.Render(identity) + "\n" + mutedStyle.Render(summary)
}

func (m Model) renderDetailPanel() string {
	header := m.renderDetailTabs()
	entry, ok := m.selectedEntry()
	if !ok {
		return header + "\n\n" + mutedStyle.Render("No streamers configured")
	}

	var body string
	switch m.visibleDetailTab() {
	case ircTab:
		body = m.renderIRCTab()
	case minerTab:
		body = m.renderMinerTab()
	default:
		body = m.renderInfoTab(entry)
	}
	return header + "\n\n" + body
}

func (m Model) renderDetailTabs() string {
	tabs := []string{
		m.renderDetailTabButton(infoTab, "Info"),
		m.renderDetailTabButton(ircTab, "Chat"),
		m.renderDetailTabButton(minerTab, "Miner"),
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

func (m Model) renderDetailTabButton(tab detailTab, label string) string {
	if m.visibleDetailTab() == tab {
		return activeTabStyle.Render(" " + label + " ")
	}
	return inactiveTabStyle.Render(" " + label + " ")
}

func (m Model) renderInfoTab(entry twitch.StreamerEntry) string {
	lines := []string{
		"Channel: " + streamerName(entry),
	}
	if entry.ChannelID != "" {
		lines = append(lines, "Channel ID: "+entry.ChannelID)
	}
	lines = append(lines, "Status: "+statusText(entry))

	detail := m.ircDetails[normalizeKey(entry.Login)]
	if !isActive(entry) {
		lines = append(lines, "Chat: "+mutedStyle.Render("inactive"))
		return strings.Join(lines, "\n")
	}

	if detail.joined {
		lines = append(lines, "Chat: "+accentStyle.Render("joined"))
	} else {
		lines = append(lines, "Chat: "+mutedStyle.Render("not joined"))
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderIRCTab() string {
	return m.ircViewport.View()
}

func (m Model) renderMinerTab() string {
	return m.minerViewport.View()
}

func (m Model) renderStreamerRows(maxRows int) string {
	entries := m.visibleStreamers(maxRows)
	if len(entries) == 0 {
		return mutedStyle.Render("none")
	}

	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		nameStyle := rowNameStyle
		if entry.ConfigLogin == m.selectedConfig {
			nameStyle = selectedRowNameStyle
		}
		label := nameStyle.Render(streamerName(entry))
		if isActive(entry) {
			label += " " + accentStyle.Render("●")
		}
		lines = append(lines, label)
	}
	return strings.Join(lines, "\n")
}

func (m Model) visibleStreamers(maxRows int) []twitch.StreamerEntry {
	entries := m.orderedStreamers()
	visible := max(1, maxRows)
	if len(entries) <= visible {
		return entries
	}

	selected := max(0, m.selectedRowIndex(entries))
	start := selected - visible + 1
	if start < 0 {
		start = 0
	}
	if start+visible > len(entries) {
		start = len(entries) - visible
	}
	return entries[start : start+visible]
}

func watchingSummary(streamers []twitch.StreamerEntry) string {
	live := make([]string, 0, 2)
	for _, streamer := range streamers {
		if streamer.Status != twitch.StreamerReady || !streamer.Live {
			continue
		}
		name := streamer.Login
		if name == "" {
			name = streamer.ConfigLogin
		}
		live = append(live, name)
		if len(live) == 2 {
			break
		}
	}
	if len(live) == 0 {
		return "no live streamers"
	}
	return strings.Join(live, ", ")
}

func statusText(entry twitch.StreamerEntry) string {
	switch {
	case entry.Status == twitch.StreamerError:
		return errorStyle.Render("error")
	case entry.Status == twitch.StreamerLoading:
		return mutedStyle.Render("loading")
	case entry.Live:
		return accentStyle.Render("live")
	default:
		return mutedStyle.Render("offline")
	}
}

func rawStatus(entry twitch.StreamerEntry) string {
	switch {
	case entry.Status == twitch.StreamerError:
		return "error"
	case entry.Status == twitch.StreamerLoading:
		return "loading"
	case entry.Live:
		return "live"
	default:
		return "offline"
	}
}

func ircSummary(detail ircDetail) string {
	if detail.joined {
		return "irc joined"
	}
	return "irc idle"
}

func contentWidth(width int) int {
	if width <= 0 {
		return defaultViewWidth
	}
	return max(24, width-4)
}

func authViewportHeight(height int) int {
	if height <= 0 {
		return 8
	}
	return max(4, height-8)
}

func streamerListWidth(width int) int {
	if width <= 0 {
		return 34
	}
	if width < 72 {
		return max(24, width/2-5)
	}
	return min(40, max(30, width/3))
}

func detailWidth(width int) int {
	if width <= 0 {
		width = defaultViewWidth
	}
	usedWidth := dashboardPageStyle.GetHorizontalPadding() +
		streamerListWidth(width) +
		panelStyle.GetHorizontalBorderSize() +
		dashboardPanelGap +
		panelStyle.GetHorizontalBorderSize()
	return max(24, width-usedWidth)
}

func detailViewportWidth(width int) int {
	return max(16, detailWidth(width)-panelStyle.GetHorizontalPadding())
}

func streamerListHeight(height int) int {
	return max(1, dashboardPanelContentHeight(height)-1)
}

func detailViewportHeight(height int, content string) int {
	contentHeight := max(1, lipgloss.Height(content))
	maxBodyHeight := max(1, dashboardPanelContentHeight(height)-detailPanelHeaderHeight)
	return max(1, min(contentHeight, maxBodyHeight))
}

func dashboardPanelContentHeight(height int) int {
	return max(1, panelHeight(height)-panelStyle.GetVerticalPadding())
}

func panelHeight(height int) int {
	if height <= 0 {
		return 16
	}
	return max(6, height-8)
}
