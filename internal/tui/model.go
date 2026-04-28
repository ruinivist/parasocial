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
)

type viewMode int

const (
	authView viewMode = iota
	streamerView
)

type StartAuthFunc func(chan<- AuthUpdate)

// AuthUpdate carries one incremental auth log line or completion result into the TUI.
type AuthUpdate struct {
	Line  string
	State *auth.State
	Err   error
	Done  bool
}

// Options configures the initial streamer list, auth state, and auth starter hook.
type Options struct {
	Streamers []string
	AuthState *auth.State
	StartAuth StartAuthFunc
}

// authStartedMsg hands the model the channel that will stream auth updates.
type authStartedMsg struct {
	Updates <-chan AuthUpdate
}

// Model is the terminal UI for auth and streamer display.
type Model struct {
	streamers   []string
	authState   *auth.State
	startAuth   StartAuthFunc
	authUpdates <-chan AuthUpdate
	authLogs    []string
	authErr     error
	mode        viewMode
}

// New returns a Bubble Tea model for auth and streamer display.
func New(options Options) Model {
	model := Model{
		streamers: append([]string(nil), options.Streamers...),
		authState: options.AuthState,
		startAuth: options.StartAuth,
		authLogs:  []string{},
		mode:      authView,
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
	return nil
}

// Update applies auth progress events and handles the global quit keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case authStartedMsg:
		m.authUpdates = msg.Updates
		return m, waitForAuthUpdate(msg.Updates)
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
			}
			return m, nil
		}
		if m.authUpdates != nil {
			return m, waitForAuthUpdate(m.authUpdates)
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

	if m.authState != nil && m.authState.Login != "" {
		fmt.Fprintf(&builder, "Logged in as %s\n\n", m.authState.Login)
	}
	builder.WriteString("Streamers\n")
	for i, streamer := range m.streamers {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, streamer)
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
