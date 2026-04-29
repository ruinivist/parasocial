package tui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"parasocial/internal/auth"
	"parasocial/internal/twitch"
)

const defaultViewWidth = 80

type viewMode int

const (
	authView viewMode = iota
	streamerView
)

type modelRuntime interface {
	startAuth(chan<- AuthUpdate)
	startResolve(*auth.State, chan<- StreamerUpdate)
}

// Model is the terminal UI for auth and streamer display.
type Model struct {
	streamers       []twitch.StreamerEntry
	authState       *auth.State
	runtime         modelRuntime
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
	authViewport    viewport.Model
}

type ircDetail struct {
	joined bool
	line   string
}

// New returns a Bubble Tea model for auth and streamer display.
func New(options Options) Model {
	streamers := twitch.LoadingStreamerEntries(options.Streamers)
	if len(options.initialStreamers) > 0 {
		streamers = append([]twitch.StreamerEntry(nil), options.initialStreamers...)
	}

	model := Model{
		streamers:    streamers,
		authState:    options.AuthState,
		runtime:      options.runtime,
		authLogs:     []string{},
		mode:         authView,
		ircDetails:   make(map[string]ircDetail),
		width:        defaultViewWidth,
		height:       24,
		authViewport: newAuthViewport(contentWidth(defaultViewWidth), authViewportHeight(24)),
	}
	if options.AuthState != nil {
		model.mode = streamerView
	}
	model.ensureSelection()
	model.syncAuthViewport()
	return model
}

// Init kicks off authentication only when the UI starts in the login state.
func (m Model) Init() tea.Cmd {
	if m.mode == authView {
		return startAuthSession(m.runtime)
	}
	return startStreamerResolution(m.runtime, m.authState)
}

// Update applies runtime progress events and handles global keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case authStartedMsg:
		m.authUpdates = msg.Updates
		return m, waitForAuthUpdate(msg.Updates)
	case streamerStartedMsg:
		m.streamerUpdates = msg.Updates
		return m, waitForStreamerUpdate(msg.Updates)
	case AuthUpdate:
		return m.updateAuth(msg)
	case StreamerUpdate:
		return m.updateStreamer(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeComponents()
		m.ensureSelection()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.mode == streamerView {
				m.moveSelection(-1)
			}
			return m, nil
		case "down", "j":
			if m.mode == streamerView {
				m.moveSelection(1)
			}
			return m, nil
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		}
	}

	if m.mode == authView {
		var cmd tea.Cmd
		m.authViewport, cmd = m.authViewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateAuth(msg AuthUpdate) (tea.Model, tea.Cmd) {
	if msg.Line != "" {
		m.authLogs = append(m.authLogs, msg.Line)
		m.syncAuthViewport()
	}
	if msg.Done {
		if msg.Err != nil {
			m.authErr = msg.Err
			m.syncAuthViewport()
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
			m.resizeComponents()
			return m, startStreamerResolution(m.runtime, m.authState)
		}
		return m, nil
	}
	if m.authUpdates != nil {
		return m, waitForAuthUpdate(m.authUpdates)
	}
	return m, nil
}

func (m Model) updateStreamer(msg StreamerUpdate) (tea.Model, tea.Cmd) {
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
	m.resizeComponents()
	if msg.Done {
		return m, nil
	}
	if m.streamerUpdates != nil {
		return m, waitForStreamerUpdate(m.streamerUpdates)
	}
	return m, nil
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
		detail.line = update.Line
	}
	m.ircDetails[login] = detail
}

func (m *Model) ensureSelection() {
	entries := m.orderedStreamers()
	if len(entries) == 0 {
		m.selectedConfig = ""
		return
	}
	if m.selectedConfig == "" {
		m.selectedConfig = entries[0].ConfigLogin
		return
	}
	for _, entry := range entries {
		if entry.ConfigLogin == m.selectedConfig {
			return
		}
	}
	m.selectedConfig = entries[0].ConfigLogin
}

func (m *Model) moveSelection(delta int) {
	entries := m.orderedStreamers()
	if len(entries) == 0 {
		m.selectedConfig = ""
		return
	}

	selected := m.selectedRowIndex(entries) + delta
	if selected < 0 {
		selected = 0
	}
	if selected >= len(entries) {
		selected = len(entries) - 1
	}
	m.selectedConfig = entries[selected].ConfigLogin
}

func (m Model) orderedStreamers() []twitch.StreamerEntry {
	entries := make([]twitch.StreamerEntry, 0, len(m.streamers))
	for _, entry := range m.streamers {
		if isActive(entry) {
			entries = append(entries, entry)
		}
	}
	for _, entry := range m.streamers {
		if isActive(entry) {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func (m Model) selectedRowIndex(entries []twitch.StreamerEntry) int {
	for index, entry := range entries {
		if entry.ConfigLogin == m.selectedConfig {
			return index
		}
	}
	if len(entries) == 0 {
		return -1
	}
	return 0
}

func (m *Model) resizeComponents() {
	m.authViewport.Width = contentWidth(m.width)
	m.authViewport.Height = authViewportHeight(m.height)
}

func (m *Model) syncAuthViewport() {
	m.authViewport.SetContent(m.authLogContent())
	m.authViewport.GotoBottom()
}

func (m Model) authLogContent() string {
	if len(m.authLogs) == 0 {
		return "Starting authentication..."
	}
	return strings.Join(m.authLogs, "\n")
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

func loadingEntries(entries []twitch.StreamerEntry) []twitch.StreamerEntry {
	logins := make([]string, 0, len(entries))
	for _, entry := range entries {
		logins = append(logins, entry.ConfigLogin)
	}
	return twitch.LoadingStreamerEntries(logins)
}

func startAuthSession(runtime modelRuntime) tea.Cmd {
	return func() tea.Msg {
		if runtime == nil {
			return AuthUpdate{Err: errors.New("auth runtime is nil"), Done: true}
		}
		updates := make(chan AuthUpdate, 32)
		runtime.startAuth(updates)
		return authStartedMsg{Updates: updates}
	}
}

func startStreamerResolution(runtime modelRuntime, state *auth.State) tea.Cmd {
	return func() tea.Msg {
		if runtime == nil {
			return StreamerUpdate{Err: errors.New("streamer runtime is nil"), Done: true}
		}
		if state == nil {
			return StreamerUpdate{Err: errors.New("auth state is nil"), Done: true}
		}
		updates := make(chan StreamerUpdate, 32)
		runtime.startResolve(state, updates)
		return streamerStartedMsg{Updates: updates}
	}
}

func waitForAuthUpdate(updates <-chan AuthUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-updates
		if !ok {
			return AuthUpdate{Err: errors.New("auth updates ended unexpectedly"), Done: true}
		}
		return update
	}
}

func waitForStreamerUpdate(updates <-chan StreamerUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-updates
		if !ok {
			return StreamerUpdate{Done: true}
		}
		return update
	}
}
