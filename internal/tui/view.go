package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"parasocial/internal/twitch"
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
	left := panelStyle.
		Width(streamerListWidth(m.width)).
		Height(panelHeight(m.height)).
		Render(labelStyle.Render("Streamers") + "\n" + m.renderStreamerRows())
	right := panelStyle.
		Width(detailWidth(m.width)).
		Height(panelHeight(m.height)).
		Render(m.renderDetailPanel())

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	return pageStyle.Render(header+"\n\n"+body) + "\n"
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
	entries := m.orderedStreamers()
	selected := m.selectedRowIndex(entries)
	if selected < 0 || selected >= len(entries) {
		return labelStyle.Render("IRC Chat") + "\n\n" + mutedStyle.Render("No streamers configured")
	}

	entry := entries[selected]
	lines := []string{
		labelStyle.Render("IRC Chat"),
		"",
		titleStyle.Render(streamerName(entry)),
		"Status: " + statusText(entry),
	}
	if entry.Error != "" {
		lines = append(lines, "Error: "+errorStyle.Render(entry.Error))
	}

	if !isActive(entry) {
		lines = append(lines, "IRC: "+mutedStyle.Render("inactive"))
		return strings.Join(lines, "\n")
	}

	detail := m.ircDetails[normalizeKey(entry.Login)]
	if detail.joined {
		lines = append(lines, "IRC: "+accentStyle.Render("joined"))
	} else {
		lines = append(lines, "IRC: "+mutedStyle.Render("not joined"))
	}
	if detail.line != "" {
		lines = append(lines, "", mutedStyle.Render(detail.line))
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderStreamerRows() string {
	entries := m.visibleStreamers()
	if len(entries) == 0 {
		return mutedStyle.Render("none")
	}

	lines := make([]string, 0, len(entries)*2)
	for _, entry := range entries {
		nameStyle, descStyle := rowNameStyle, rowDescStyle
		if entry.ConfigLogin == m.selectedConfig {
			nameStyle, descStyle = selectedRowNameStyle, selectedRowDescStyle
		}
		detail := m.ircDetails[normalizeKey(entry.Login)]
		lines = append(lines,
			nameStyle.Render(streamerName(entry)),
			descStyle.Render(rawStatus(entry)+" | "+ircSummary(detail)),
		)
	}
	return strings.Join(lines, "\n")
}

func (m Model) visibleStreamers() []twitch.StreamerEntry {
	entries := m.orderedStreamers()
	visible := max(1, streamerListHeight(m.height)/2)
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
		return statusErrorStyle.Render("error")
	case entry.Status == twitch.StreamerLoading:
		return mutedStyle.Render("loading")
	case entry.Live:
		return statusLiveStyle.Render("live")
	default:
		return statusIdleStyle.Render("offline")
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
		return defaultViewWidth - streamerListWidth(width) - 7
	}
	return max(24, width-streamerListWidth(width)-11)
}

func streamerListHeight(height int) int {
	if height <= 0 {
		return 14
	}
	return max(4, height-10)
}

func panelHeight(height int) int {
	if height <= 0 {
		return 16
	}
	return max(6, height-8)
}
