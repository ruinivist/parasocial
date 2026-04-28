// app.go wires together config loading, auth bootstrap, and the Bubble Tea program.
// It decides whether cached Twitch auth can be reused and, when needed,
// connects the interactive login flow to the TUI through auth update messages.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"parasocial/internal/auth"
	"parasocial/internal/config"
	"parasocial/internal/gql"
	"parasocial/internal/irc"
	"parasocial/internal/tui"
	"parasocial/internal/twitch"
)

const streamerRefreshInterval = 5 * time.Minute

// Run loads the application configuration and starts the terminal UI.
func Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	authClient := auth.NewClient(httpClient)
	authPath := auth.DefaultPath()

	state, err := authClient.ReuseAuth(ctx, authPath)
	if err != nil {
		return err
	}

	program := tea.NewProgram(
		tui.New(tui.Options{
			Streamers: twitch.LoadingStreamerEntries(cfg.Streamers),
			AuthState: state,
			StartAuth: func(ch chan<- tui.AuthUpdate) {
				go func() {
					defer close(ch)

					state, err := authClient.EnsureAuth(ctx, authPath, func(line string) {
						ch <- tui.AuthUpdate{Line: strings.TrimRight(line, "\n")}
					})
					if err != nil {
						ch <- tui.AuthUpdate{
							Line: fmt.Sprintf("Authentication failed: %v", err),
							Err:  err,
							Done: true,
						}
						return
					}

					ch <- tui.AuthUpdate{State: state, Done: true}
				}()
			},
			StartResolve: func(state *auth.State, ch chan<- tui.StreamerUpdate) {
				go func() {
					defer close(ch)
					ircManager := newIRCManager(ch)
					defer ircManager.Close()

					service, err := newTwitchService(httpClient, state)
					if err != nil {
						ch <- tui.StreamerUpdate{Err: err, Done: true}
						return
					}
					if err := resolveStreamerEntries(ctx, service, state, cfg.Streamers, ircManager, func(update tui.StreamerUpdate) {
						ch <- update
					}); err != nil {
						if errors.Is(err, context.Canceled) {
							return
						}
						ch <- tui.StreamerUpdate{Err: err, Done: true}
						return
					}
					ch <- tui.StreamerUpdate{Done: true}
				}()
			},
		}),
		tea.WithContext(ctx),
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
	)
	_, err = program.Run()
	return err
}

// streamerService captures the Twitch lookups the app needs during UI resolution.
type streamerService interface {
	CurrentUser(context.Context) (*twitch.Viewer, error)
	ResolveStreamer(context.Context, string) (*twitch.Channel, error)
	StreamInfo(context.Context, string) (*twitch.StreamInfo, error)
	PlaybackAccessToken(context.Context, string) (*twitch.PlaybackToken, error)
}

type ircSyncer interface {
	Sync(context.Context, string, string, []irc.Target)
}

// newTwitchService builds a Twitch service from the authenticated session state.
func newTwitchService(httpClient *http.Client, state *auth.State) (*twitch.Service, error) {
	client := &gql.Client{
		HTTPClient: httpClient,
		Session: gql.Session{
			AccessToken: state.AccessToken,
			ClientID:    state.ClientID,
			DeviceID:    state.DeviceID,
			UserAgent:   auth.TVUserAgent(),
		},
	}
	if err := client.Validate(); err != nil {
		return nil, fmt.Errorf("configure graphql session: %w", err)
	}
	return &twitch.Service{GQL: client}, nil
}

func newIRCManager(ch chan<- tui.StreamerUpdate) *irc.Manager {
	return &irc.Manager{
		Addr: irc.DefaultAddr,
		Events: func(event irc.Event) {
			ch <- tui.StreamerUpdate{
				IRC: &tui.IRCUpdate{
					Login: event.Streamer,
					State: tui.IRCState(event.State),
					Line:  event.Line,
				},
			}
		},
	}
}

// resolveStreamerEntries streams viewer and streamer resolution results into the TUI.
func resolveStreamerEntries(ctx context.Context, service streamerService, state *auth.State, logins []string, syncer ircSyncer, send func(tui.StreamerUpdate)) error {
	return resolveStreamerEntriesWithSleep(ctx, service, state, logins, syncer, send, streamerRefreshInterval, sleepContext)
}

func resolveStreamerEntriesWithSleep(ctx context.Context, service streamerService, state *auth.State, logins []string, syncer ircSyncer, send func(tui.StreamerUpdate), interval time.Duration, sleep func(context.Context, time.Duration) error) error {
	viewer, err := service.CurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}
	send(tui.StreamerUpdate{Viewer: viewer})

	entries := twitch.LoadingStreamerEntries(logins)
	syncWatchedChannels(ctx, syncer, state, entries)

	for {
		for index, login := range logins {
			entry, err := resolveStreamerEntry(ctx, service, login)
			if errors.Is(err, context.Canceled) {
				return err
			}
			entries[index] = *entry
			send(tui.StreamerUpdate{
				Index: index,
				Entry: entry,
			})
			syncWatchedChannels(ctx, syncer, state, entries)
		}
		if err := sleep(ctx, interval); err != nil {
			return err
		}
	}
}

func resolveStreamerEntry(ctx context.Context, service streamerService, login string) (*twitch.StreamerEntry, error) {
	entry := &twitch.StreamerEntry{
		ConfigLogin: login,
		Status:      twitch.StreamerLoading,
	}

	channel, err := service.ResolveStreamer(ctx, login)
	switch {
	case err == nil:
		entry.Login = channel.Login
		entry.ChannelID = channel.ID
	case errors.Is(err, context.Canceled):
		return nil, err
	default:
		entry.Status = twitch.StreamerError
		entry.Error = err.Error()
		return entry, nil
	}

	stream, err := service.StreamInfo(ctx, channel.ID)
	switch {
	case err == nil:
		entry.Status = twitch.StreamerReady
		entry.Live = stream.Online
	case errors.Is(err, context.Canceled):
		return nil, err
	default:
		entry.Status = twitch.StreamerError
		entry.Error = err.Error()
		return entry, nil
	}

	if !entry.Live {
		return entry, nil
	}
	if _, err := service.PlaybackAccessToken(ctx, channel.Login); err != nil && errors.Is(err, context.Canceled) {
		return nil, err
	}
	return entry, nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func syncWatchedChannels(ctx context.Context, syncer ircSyncer, state *auth.State, entries []twitch.StreamerEntry) {
	if syncer == nil || state == nil {
		return
	}
	syncer.Sync(ctx, state.Login, state.AccessToken, watchedIRCTargets(entries))
}

func watchedIRCTargets(entries []twitch.StreamerEntry) []irc.Target {
	targets := make([]irc.Target, 0, 2)
	for _, entry := range entries {
		if entry.Status != twitch.StreamerReady || !entry.Live || entry.Login == "" {
			continue
		}
		targets = append(targets, irc.Target{Login: entry.Login})
		if len(targets) == 2 {
			break
		}
	}
	return targets
}
