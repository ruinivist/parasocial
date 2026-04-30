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
const maxIRCMessageHistory = 50

type viewMode int

const (
	authView viewMode = iota
	streamerView
)

type panelFocus int

const (
	focusStreamers panelFocus = iota
	focusInfo
	focusIRC
	focusMiner
)

type detailTab int

const (
	infoTab detailTab = iota
	ircTab
	minerTab
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
	focus           panelFocus
	ircDetails      map[string]ircDetail
	minerDetails    map[string][]string
	minerStatuses   map[string]MinerStatus
	authViewport    viewport.Model
	ircViewport     viewport.Model
	minerViewport   viewport.Model
}

type ircDetail struct {
	joined   bool
	messages []string
}

// New returns a Bubble Tea model for auth and streamer display.
func New(options Options) Model {
	streamers := twitch.LoadingStreamerEntries(options.Streamers)
	if len(options.initialStreamers) > 0 {
		streamers = append([]twitch.StreamerEntry(nil), options.initialStreamers...)
	}

	model := Model{
		streamers:     streamers,
		authState:     options.AuthState,
		runtime:       options.runtime,
		authLogs:      []string{},
		mode:          authView,
		ircDetails:    make(map[string]ircDetail),
		minerDetails:  make(map[string][]string),
		minerStatuses: make(map[string]MinerStatus),
		width:         defaultViewWidth,
		height:        24,
		focus:         focusStreamers,
		authViewport:  newAuthViewport(contentWidth(defaultViewWidth), authViewportHeight(24)),
		ircViewport:   newIRCViewport(detailViewportWidth(defaultViewWidth), detailViewportHeight(24, "")),
		minerViewport: newMinerViewport(
			detailViewportWidth(defaultViewWidth),
			detailViewportHeight(24, ""),
		),
	}
	if options.AuthState != nil {
		model.mode = streamerView
	}
	model.ensureSelection()
	model.syncAuthViewport()
	model.syncDetailViewports(true)
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
		m.syncDetailViewports(false)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "left":
			if m.mode == streamerView {
				m.moveFocusLeft()
			}
			return m, nil
		case "right":
			if m.mode == streamerView {
				m.moveFocusRight()
			}
			return m, nil
		case "up", "k":
			if m.mode == streamerView && m.focus == focusStreamers {
				m.moveSelection(-1)
				return m, nil
			}
			if m.mode == streamerView && m.focus == focusInfo {
				return m, nil
			}
		case "down", "j":
			if m.mode == streamerView && m.focus == focusStreamers {
				m.moveSelection(1)
				return m, nil
			}
			if m.mode == streamerView && m.focus == focusInfo {
				return m, nil
			}
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		}
	}

	if m.mode == authView {
		var cmd tea.Cmd
		m.authViewport, cmd = m.authViewport.Update(msg)
		return m, cmd
	}
	if m.focus == focusIRC {
		var cmd tea.Cmd
		m.ircViewport, cmd = m.ircViewport.Update(msg)
		return m, cmd
	}
	if m.focus == focusMiner {
		var cmd tea.Cmd
		m.minerViewport, cmd = m.minerViewport.Update(msg)
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
			m.minerDetails = make(map[string][]string)
			m.minerStatuses = make(map[string]MinerStatus)
			m.selectedConfig = ""
			m.focus = focusStreamers
			m.ensureSelection()
			m.resizeComponents()
			m.syncDetailViewports(true)
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
	if msg.Miner != nil {
		m.applyMinerUpdate(*msg.Miner)
	}
	if msg.Err != nil {
		m.resolveErr = msg.Err
	}
	m.ensureSelection()
	m.resizeComponents()
	m.syncDetailViewports(false)
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
	if message, ok := formatIRCChatLine(update.Line); ok {
		detail.messages = appendCappedHistory(detail.messages, message)
	}
	m.ircDetails[login] = detail
}

func (m *Model) applyMinerUpdate(update MinerUpdate) {
	login := normalizeKey(update.Login)
	if login == "" {
		return
	}
	if update.Status != nil {
		m.minerStatuses[login] = *update.Status
	}
	line := strings.TrimSpace(update.Line)
	if line != "" {
		m.minerDetails[login] = appendCappedHistory(m.minerDetails[login], line)
	}
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
		m.syncDetailViewports(true)
		return
	}

	current := m.selectedConfig
	selected := m.selectedRowIndex(entries) + delta
	if selected < 0 {
		selected = 0
	}
	if selected >= len(entries) {
		selected = len(entries) - 1
	}
	m.selectedConfig = entries[selected].ConfigLogin
	if m.selectedConfig != current {
		m.syncDetailViewports(true)
	}
}

func (m *Model) moveFocusLeft() {
	switch m.focus {
	case focusInfo:
		m.focus = focusStreamers
	case focusIRC:
		m.focus = focusInfo
	case focusMiner:
		m.focus = focusIRC
	}
}

func (m *Model) moveFocusRight() {
	switch m.focus {
	case focusStreamers:
		m.focus = focusInfo
	case focusInfo:
		m.focus = focusIRC
	case focusIRC:
		m.focus = focusMiner
	}
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
	m.ircViewport.Width = detailViewportWidth(m.width)
	m.ircViewport.Height = detailViewportHeight(m.height, m.ircViewportContent())
	m.minerViewport.Width = detailViewportWidth(m.width)
	m.minerViewport.Height = detailViewportHeight(m.height, m.minerViewportContent())
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

func (m *Model) syncIRCViewport(forceBottom bool) {
	stickToBottom := forceBottom || m.ircViewport.AtBottom()
	content := m.ircViewportContent()
	m.ircViewport.Width = detailViewportWidth(m.width)
	m.ircViewport.Height = detailViewportHeight(m.height, content)
	if m.ircViewport.Width <= 0 || m.ircViewport.Height <= 0 {
		return
	}

	m.ircViewport.SetContent(content)
	if stickToBottom {
		m.ircViewport.GotoBottom()
	}
}

func (m *Model) syncMinerViewport(forceBottom bool) {
	stickToBottom := forceBottom || m.minerViewport.AtBottom()
	content := m.minerViewportContent()
	m.minerViewport.Width = detailViewportWidth(m.width)
	m.minerViewport.Height = detailViewportHeight(m.height, content)
	if m.minerViewport.Width <= 0 || m.minerViewport.Height <= 0 {
		return
	}

	m.minerViewport.SetContent(content)
	if stickToBottom {
		m.minerViewport.GotoBottom()
	}
}

func (m *Model) syncDetailViewports(forceBottom bool) {
	m.syncIRCViewport(forceBottom)
	m.syncMinerViewport(forceBottom)
}

func (m Model) ircViewportContent() string {
	entry, ok := m.selectedEntry()
	if !ok {
		return "No streamers configured"
	}
	if !isActive(entry) {
		return "Chat becomes available when this streamer is live."
	}

	detail := m.ircDetails[normalizeKey(entry.Login)]
	if !detail.joined {
		return "Connecting to Twitch IRC..."
	}
	if len(detail.messages) == 0 {
		return "Waiting for chat messages..."
	}
	return strings.Join(detail.messages, "\n")
}

func (m Model) minerViewportContent() string {
	entry, ok := m.selectedEntry()
	if !ok {
		return "No streamers configured"
	}

	logins := m.minerDetails[normalizeKey(entry.Login)]
	if len(logins) == 0 {
		return "Waiting for miner activity..."
	}
	return strings.Join(logins, "\n")
}

func (m Model) selectedEntry() (twitch.StreamerEntry, bool) {
	entries := m.orderedStreamers()
	selected := m.selectedRowIndex(entries)
	if selected < 0 || selected >= len(entries) {
		return twitch.StreamerEntry{}, false
	}
	return entries[selected], true
}

func (m Model) visibleDetailTab() detailTab {
	switch m.focus {
	case focusIRC:
		return ircTab
	case focusMiner:
		return minerTab
	default:
		return infoTab
	}
}

func (m Model) isStreamersFocused() bool {
	return m.focus == focusStreamers
}

func (m Model) isRightPanelFocused() bool {
	return m.focus == focusInfo || m.focus == focusIRC || m.focus == focusMiner
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

func formatIRCChatLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}

	parts := strings.SplitN(line, " :", 2)
	if len(parts) != 2 || !strings.Contains(parts[0], " PRIVMSG ") {
		return "", false
	}

	prefix := strings.TrimPrefix(parts[0], ":")
	user, _, _ := strings.Cut(prefix, "!")
	if user == "" {
		user = "unknown"
	}
	message := strings.TrimSpace(parts[1])
	if message == "" {
		return "", false
	}
	return user + ": " + message, true
}

func appendCappedHistory(history []string, line string) []string {
	history = append(history, line)
	if len(history) <= maxIRCMessageHistory {
		return history
	}
	return append([]string(nil), history[len(history)-maxIRCMessageHistory:]...)
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
