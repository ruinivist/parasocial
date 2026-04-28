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

type viewMode int

const (
	authView viewMode = iota
	streamerView
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
	Index  int
	Err    error
	Done   bool
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
	}
	if options.AuthState != nil {
		model.mode = streamerView
	}
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
		if msg.Err != nil {
			m.resolveErr = msg.Err
		}
		if msg.Done {
			return m, nil
		}
		if m.streamerUpdates != nil {
			return m, waitForStreamerUpdate(m.streamerUpdates)
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		}
	}

	return m, nil
}

// View renders either the login log screen or the authenticated streamer list.
func (m Model) View() string {
	var builder strings.Builder
	if m.mode == authView {
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

	switch {
	case m.viewer != nil:
		fmt.Fprintf(&builder, "Logged in as %s (%s)\n\n", m.viewer.Login, m.viewer.ID)
	case m.authState != nil && m.authState.Login != "":
		fmt.Fprintf(&builder, "Logged in as %s\n", m.authState.Login)
		if m.resolveErr != nil {
			fmt.Fprintf(&builder, "Viewer lookup failed: %v\n\n", m.resolveErr)
		} else {
			builder.WriteString("Resolving viewer identity...\n\n")
		}
	}
	builder.WriteString("Streamers\n")
	for i, streamer := range m.streamers {
		switch streamer.Status {
		case twitch.StreamerReady:
			fmt.Fprintf(&builder, "%d. %s (%s)\n", i+1, streamer.Login, streamer.ChannelID)
		case twitch.StreamerError:
			fmt.Fprintf(&builder, "%d. %s [error: %s]\n", i+1, streamer.ConfigLogin, streamer.Error)
		default:
			fmt.Fprintf(&builder, "%d. %s [loading]\n", i+1, streamer.ConfigLogin)
		}
	}
	builder.WriteString("\n")
	return builder.String()
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
