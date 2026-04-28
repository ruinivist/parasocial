// model.go contains the Bubble Tea state machine for the current terminal UI.
// It switches between the Twitch login log view and the streamer list view,
// and applies auth progress messages emitted by the app layer.
package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"parasocial/internal/auth"
	"parasocial/internal/twitch"
)

const defaultViewWidth = 80
const greenDot = "\x1b[32m●\x1b[0m"

type viewMode int

const (
	authView viewMode = iota
	streamerView
)

// IRCState describes the current IRC join lifecycle for one streamer row.
type IRCState string

const (
	IRCPending      IRCState = "pending"
	IRCJoined       IRCState = "joined"
	IRCDisconnected IRCState = "disconnected"
)

// StartAuthFunc begins the asynchronous Twitch login flow for the model.
type StartAuthFunc func(chan<- AuthUpdate)

// StartResolveFunc begins the asynchronous viewer and streamer resolution flow for the model.
type StartResolveFunc func(*auth.State, chan<- StreamerUpdate)

// AuthUpdate carries one incremental auth log line or completion result into the TUI.
type AuthUpdate struct {
	Line  string
	State *auth.State
	Err   error
	Done  bool
}

// StreamerUpdate carries one streamer resolution update into the TUI.
type StreamerUpdate struct {
	Viewer *twitch.Viewer
	Entry  *twitch.StreamerEntry
	IRC    *IRCUpdate
	Index  int
	Err    error
	Done   bool
}

// IRCUpdate carries one IRC connection state or log line into the TUI.
type IRCUpdate struct {
	Login string
	State IRCState
	Line  string
}

// Options configures the initial streamer list, auth state, and worker starter hooks.
type Options struct {
	Streamers    []twitch.StreamerEntry
	AuthState    *auth.State
	StartAuth    StartAuthFunc
	StartResolve StartResolveFunc
}

// authStartedMsg hands the model the channel that will stream auth updates.
type authStartedMsg struct {
	Updates <-chan AuthUpdate
}

// streamerStartedMsg hands the model the channel that will stream streamer updates.
type streamerStartedMsg struct {
	Updates <-chan StreamerUpdate
}

// Model is the terminal UI for auth and streamer display.
type Model struct {
	streamers       []twitch.StreamerEntry
	authState       *auth.State
	startAuth       StartAuthFunc
	startResolve    StartResolveFunc
	authUpdates     <-chan AuthUpdate
	streamerUpdates <-chan StreamerUpdate
	authLogs        []string
	authErr         error
	resolveErr      error
	viewer          *twitch.Viewer
	mode            viewMode
	width           int
	height          int
	selectedConfig  string
	ircDetails      map[string]ircDetail
}

type ircDetail struct {
	joined bool
}

type streamerRow struct {
	index int
	entry twitch.StreamerEntry
}

// New returns a Bubble Tea model for auth and streamer display.
func New(options Options) Model {
	model := Model{
		streamers:    append([]twitch.StreamerEntry(nil), options.Streamers...),
		authState:    options.AuthState,
		startAuth:    options.StartAuth,
		startResolve: options.StartResolve,
		authLogs:     []string{},
		mode:         authView,
		ircDetails:   make(map[string]ircDetail),
	}
	if options.AuthState != nil {
		model.mode = streamerView
	}
	model.ensureSelection()
	return model
}

// Init kicks off authentication only when the UI starts in the login state.
func (m Model) Init() tea.Cmd {
	if m.mode == authView {
		return startAuthSession(m.startAuth)
	}
	return startStreamerResolution(m.startResolve, m.authState)
}

// Update applies auth progress events and handles the global quit keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case authStartedMsg:
		m.authUpdates = msg.Updates
		return m, waitForAuthUpdate(msg.Updates)
	case streamerStartedMsg:
		m.streamerUpdates = msg.Updates
		return m, waitForStreamerUpdate(msg.Updates)
	case AuthUpdate:
		if msg.Line != "" {
			m.authLogs = append(m.authLogs, msg.Line)
		}
		if msg.Done {
			if msg.Err != nil {
				m.authErr = msg.Err
				return m, nil
			}
			if msg.State != nil {
				m.authState = msg.State
				m.authErr = nil
				m.mode = streamerView
				m.viewer = nil
				m.resolveErr = nil
				m.streamers = loadingEntries(m.streamers)
				m.ircDetails = make(map[string]ircDetail)
				m.selectedConfig = ""
				m.ensureSelection()
				return m, startStreamerResolution(m.startResolve, m.authState)
			}
			return m, nil
		}
		if m.authUpdates != nil {
			return m, waitForAuthUpdate(m.authUpdates)
		}
	case StreamerUpdate:
		if msg.Viewer != nil {
			m.viewer = msg.Viewer
			m.resolveErr = nil
		}
		if msg.Entry != nil && msg.Index >= 0 && msg.Index < len(m.streamers) {
			m.streamers[msg.Index] = *msg.Entry
		}
		if msg.IRC != nil {
			m.applyIRCUpdate(*msg.IRC)
		}
		if msg.Err != nil {
			m.resolveErr = msg.Err
		}
		m.ensureSelection()
		if msg.Done {
			return m, nil
		}
		if m.streamerUpdates != nil {
			return m, waitForStreamerUpdate(m.streamerUpdates)
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureSelection()
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.mode == streamerView {
				m.moveSelection(-1)
			}
		case "down", "j":
			if m.mode == streamerView {
				m.moveSelection(1)
			}
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		}
	}

	return m, nil
}

// View renders either the login log screen or the authenticated streamer list.
func (m Model) View() string {
	if m.mode == authView {
		var builder strings.Builder
		builder.WriteString("Twitch Login\n")
		if len(m.authLogs) == 0 {
			builder.WriteString("Starting authentication...\n")
		} else {
			for _, line := range m.authLogs {
				builder.WriteString(line)
				builder.WriteByte('\n')
			}
		}
		if m.authErr != nil {
			builder.WriteString("\nLogin did not complete. Press q to quit.\n")
		}
		builder.WriteString("\n")
		return builder.String()
	}

	return m.renderStreamerView()
}

func (m Model) renderStreamerView() string {
	var builder strings.Builder
	switch {
	case m.viewer != nil:
		fmt.Fprintf(&builder, "Logged in as %s (%s)\n", m.viewer.Login, m.viewer.ID)
	case m.authState != nil && m.authState.Login != "":
		fmt.Fprintf(&builder, "Logged in as %s\n", m.authState.Login)
		if m.resolveErr != nil {
			fmt.Fprintf(&builder, "Viewer lookup failed: %v\n", m.resolveErr)
		} else {
			builder.WriteString("Resolving viewer identity...\n")
		}
	}
	fmt.Fprintf(&builder, "Watching: %s\n", watchingSummary(m.streamers))

	rows := m.orderedRows()
	selectedIndex := m.selectedRowIndex(rows)
	leftPane := m.leftPaneLines(rows)
	rightPane := m.rightPaneLines(rows, selectedIndex)

	if m.height > 0 {
		available := m.height - 3
		if available > 0 {
			leftPane = clipListLines(leftPane, available, selectedIndex+1)
			rightPane = clipTailLines(rightPane, available)
		}
	}

	bodyWidth := m.width
	if bodyWidth <= 0 {
		bodyWidth = defaultViewWidth
	}

	builder.WriteString("\n")
	for _, line := range renderColumns(leftPane, rightPane, bodyWidth) {
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	builder.WriteByte('\n')
	return builder.String()
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

func (m *Model) applyIRCUpdate(update IRCUpdate) {
	login := normalizeKey(update.Login)
	if login == "" {
		return
	}

	detail := m.ircDetails[login]
	switch update.State {
	case IRCJoined:
		detail.joined = true
	case IRCPending, IRCDisconnected:
		detail.joined = false
	}
	if update.Line != "" {
	}
	m.ircDetails[login] = detail
}

func (m *Model) ensureSelection() {
	rows := m.orderedRows()
	if len(rows) == 0 {
		m.selectedConfig = ""
		return
	}
	if m.selectedConfig == "" {
		m.selectedConfig = rows[0].entry.ConfigLogin
		return
	}
	for _, row := range rows {
		if row.entry.ConfigLogin == m.selectedConfig {
			return
		}
	}
	m.selectedConfig = rows[0].entry.ConfigLogin
}

func (m *Model) moveSelection(delta int) {
	rows := m.orderedRows()
	if len(rows) == 0 {
		m.selectedConfig = ""
		return
	}
	m.ensureSelection()

	selected := m.selectedRowIndex(rows)
	if selected < 0 {
		selected = 0
	}
	selected += delta
	if selected < 0 {
		selected = 0
	}
	if selected >= len(rows) {
		selected = len(rows) - 1
	}
	m.selectedConfig = rows[selected].entry.ConfigLogin
}

func (m Model) orderedRows() []streamerRow {
	rows := make([]streamerRow, 0, len(m.streamers))
	for index, entry := range m.streamers {
		if isActive(entry) {
			rows = append(rows, streamerRow{index: index, entry: entry})
		}
	}
	for index, entry := range m.streamers {
		if isActive(entry) {
			continue
		}
		rows = append(rows, streamerRow{index: index, entry: entry})
	}
	return rows
}

func (m Model) selectedRowIndex(rows []streamerRow) int {
	for index, row := range rows {
		if row.entry.ConfigLogin == m.selectedConfig {
			return index
		}
	}
	if len(rows) == 0 {
		return -1
	}
	return 0
}

func (m Model) leftPaneLines(rows []streamerRow) []string {
	lines := []string{"Streamers"}
	for _, row := range rows {
		prefix := "  "
		if row.entry.ConfigLogin == m.selectedConfig {
			prefix = "> "
		}

		status := "inactive"
		if isActive(row.entry) {
			status = "active"
		}
		lines = append(lines, prefix+streamerName(row.entry)+" ["+status+"]")
	}
	if len(rows) == 0 {
		lines = append(lines, "  none")
	}
	return lines
}

func (m Model) rightPaneLines(rows []streamerRow, selected int) []string {
	lines := []string{"IRC Chat"}
	if selected < 0 || selected >= len(rows) {
		return append(lines, "")
	}

	row := rows[selected].entry
	if !isActive(row) {
		return append(lines, "inactive")
	}

	detail := m.ircDetails[normalizeKey(row.Login)]
	if !detail.joined {
		return append(lines, "not joined")
	}
	return append(lines, greenDot)
}

func isActive(entry twitch.StreamerEntry) bool {
	return entry.Status == twitch.StreamerReady && entry.Live
}

func streamerName(entry twitch.StreamerEntry) string {
	if entry.Login != "" {
		return entry.Login
	}
	return entry.ConfigLogin
}

func normalizeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func renderColumns(left, right []string, totalWidth int) []string {
	if totalWidth <= 0 {
		totalWidth = defaultViewWidth
	}

	leftWidth, rightWidth := splitWidths(totalWidth)
	height := len(left)
	if len(right) > height {
		height = len(right)
	}

	lines := make([]string, 0, height)
	for index := 0; index < height; index++ {
		leftLine := ""
		if index < len(left) {
			leftLine = left[index]
		}
		rightLine := ""
		if index < len(right) {
			rightLine = right[index]
		}
		lines = append(lines, fitWidth(leftLine, leftWidth)+" │ "+fitWidth(rightLine, rightWidth))
	}
	return lines
}

func splitWidths(totalWidth int) (int, int) {
	if totalWidth < 8 {
		return max(1, totalWidth-4), 1
	}

	leftWidth := totalWidth / 3
	if leftWidth < 20 {
		leftWidth = 20
	}
	if leftWidth > 32 {
		leftWidth = 32
	}

	rightWidth := totalWidth - leftWidth - 3
	if rightWidth < 8 {
		rightWidth = 8
		leftWidth = totalWidth - rightWidth - 3
		if leftWidth < 1 {
			leftWidth = 1
			rightWidth = max(1, totalWidth-leftWidth-3)
		}
	}
	return leftWidth, rightWidth
}

func fitWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}

	runes := []rune(value)
	if len(runes) > width {
		return string(runes[:width])
	}
	return value + strings.Repeat(" ", width-len(runes))
}

func clipListLines(lines []string, height, selectedLine int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	if height == 1 {
		return lines[:1]
	}

	rows := lines[1:]
	visibleRows := height - 1
	selectedRow := selectedLine - 1
	if selectedRow < 0 {
		selectedRow = 0
	}
	start := 0
	if selectedRow >= visibleRows {
		start = selectedRow - visibleRows + 1
	}
	if start+visibleRows > len(rows) {
		start = len(rows) - visibleRows
	}
	if start < 0 {
		start = 0
	}
	return append([]string{lines[0]}, rows[start:start+visibleRows]...)
}

func clipTailLines(lines []string, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	if height == 1 {
		return lines[:1]
	}

	body := lines[1:]
	visibleRows := height - 1
	if len(body) > visibleRows {
		body = body[len(body)-visibleRows:]
	}
	return append([]string{lines[0]}, body...)
}

// startAuthSession starts the background auth worker and returns its update channel to Bubble Tea.
func startAuthSession(start StartAuthFunc) tea.Cmd {
	return func() tea.Msg {
		if start == nil {
			return AuthUpdate{Err: errors.New("auth start function is nil"), Done: true}
		}
		updates := make(chan AuthUpdate, 32)
		start(updates)
		return authStartedMsg{Updates: updates}
	}
}

// startStreamerResolution starts the background streamer resolver and returns its update channel.
func startStreamerResolution(start StartResolveFunc, state *auth.State) tea.Cmd {
	return func() tea.Msg {
		if start == nil {
			return StreamerUpdate{Err: errors.New("streamer resolution start function is nil"), Done: true}
		}
		if state == nil {
			return StreamerUpdate{Err: errors.New("auth state is nil"), Done: true}
		}
		updates := make(chan StreamerUpdate, 32)
		start(state, updates)
		return streamerStartedMsg{Updates: updates}
	}
}

// waitForAuthUpdate blocks until the next auth update is available from the worker.
func waitForAuthUpdate(updates <-chan AuthUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-updates
		if !ok {
			return AuthUpdate{Err: errors.New("auth updates ended unexpectedly"), Done: true}
		}
		return update
	}
}

// waitForStreamerUpdate blocks until the next streamer update is available from the worker.
func waitForStreamerUpdate(updates <-chan StreamerUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-updates
		if !ok {
			return StreamerUpdate{Done: true}
		}
		return update
	}
}

// loadingEntries resets the UI rows back to their initial loading state.
func loadingEntries(entries []twitch.StreamerEntry) []twitch.StreamerEntry {
	logins := make([]string, 0, len(entries))
	for _, entry := range entries {
		logins = append(logins, entry.ConfigLogin)
	}
	return twitch.LoadingStreamerEntries(logins)
}
