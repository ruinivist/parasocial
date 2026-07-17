package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"parasocial/internal/auth"
	"parasocial/internal/gql"
	"parasocial/internal/irc"
	"parasocial/internal/miner"
	"parasocial/internal/twitch"
)

const streamerRefreshInterval = 5 * time.Minute

// Options configures the TUI runtime.
type Options struct {
	Streamers []string
	AuthState *auth.State

	runtime          modelRuntime
	initialStreamers []twitch.StreamerEntry
}

type runtime struct {
	ctx        context.Context
	logins     []string
	httpClient *http.Client
	authClient *auth.Client
}

// Run starts the Bubble Tea UI and keeps application orchestration inside the TUI package.
func Run(ctx context.Context, options Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpClient := &http.Client{Timeout: 30 * time.Second}
	rt := &runtime{
		ctx:        ctx,
		logins:     append([]string(nil), options.Streamers...),
		httpClient: httpClient,
		authClient: auth.NewClient(httpClient),
	}
	state := options.AuthState
	if state == nil {
		var err error
		state, err = rt.authClient.ReuseAuth(rt.ctx, auth.DefaultPath())
		if err != nil {
			return err
		}
	}

	options.AuthState = state
	options.runtime = rt

	program := tea.NewProgram(
		New(options),
		tea.WithContext(ctx),
	)
	_, err := program.Run()
	return err
}

func (r *runtime) startAuth(ch chan<- AuthUpdate) {
	go func() {
		defer close(ch)

		state, err := r.authClient.EnsureAuth(r.ctx, auth.DefaultPath(), func(line string) {
			ch <- AuthUpdate{Line: strings.TrimRight(line, "\n")}
		})
		if err != nil {
			ch <- AuthUpdate{
				Line: fmt.Sprintf("Authentication failed: %v", err),
				Err:  err,
				Done: true,
			}
			return
		}

		ch <- AuthUpdate{State: state, Done: true}
	}()
}

func (r *runtime) startResolve(state *auth.State, ch chan<- StreamerUpdate) {
	go func() {
		defer close(ch)
		ircManager := &irc.Manager{Events: func(event irc.Event) {
			ch <- StreamerUpdate{IRC: &event}
		}}
		defer ircManager.Close()

		client := &gql.Client{
			HTTPClient: r.httpClient,
			Session: gql.Session{
				AccessToken: state.AccessToken,
				ClientID:    auth.ClientID,
				DeviceID:    state.DeviceID,
				UserAgent:   auth.TVUserAgent,
			},
		}
		if err := client.Validate(); err != nil {
			ch <- StreamerUpdate{Err: fmt.Errorf("configure graphql session: %w", err), Done: true}
			return
		}
		service := &twitch.Service{
			GQL:        client,
			HTTPClient: r.httpClient,
			Session:    client.Session,
		}
		minerManager := miner.NewManager(r.ctx, service, nil, func(entry miner.LogEntry) {
			ch <- StreamerUpdate{MinerLog: &entry}
		})
		minerManager.SetStatusSink(func(entry miner.StatusEntry) {
			ch <- StreamerUpdate{MinerStatus: &entry}
		})
		defer minerManager.Close()

		if err := resolveStreamerEntriesWithSleep(r.ctx, service, state, r.logins, ircManager, minerManager, func(update StreamerUpdate) {
			ch <- update
		}, streamerRefreshInterval, sleepContext); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			ch <- StreamerUpdate{Err: err, Done: true}
			return
		}
		ch <- StreamerUpdate{Done: true}
	}()
}

type streamerService interface {
	CurrentUser(context.Context) (*twitch.Viewer, error)
	ResolveStreamer(context.Context, string) (*twitch.Channel, error)
	StreamInfo(context.Context, string) (*twitch.StreamInfo, error)
	WatchStreak(context.Context, string) (*int, error)
	PlaybackAccessToken(context.Context, string) (*twitch.PlaybackToken, error)
}

type ircSyncer interface {
	Sync(context.Context, string, string, []string)
}

type minerSyncer interface {
	Sync(context.Context, *auth.State, *twitch.Viewer, []twitch.StreamerEntry)
}

func resolveStreamerEntriesWithSleep(ctx context.Context, service streamerService, state *auth.State, logins []string, ircSyncer ircSyncer, minerSyncer minerSyncer, send func(StreamerUpdate), interval time.Duration, sleep func(context.Context, time.Duration) error) error {
	viewer, err := service.CurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("resolve current user: %w", err)
	}
	send(StreamerUpdate{Viewer: viewer})

	entries := twitch.LoadingStreamerEntries(logins)
	syncRuntimeState(ctx, ircSyncer, minerSyncer, state, viewer, entries)

	for {
		for index, login := range logins {
			entry, err := resolveStreamerEntry(ctx, service, login)
			if errors.Is(err, context.Canceled) {
				return err
			}
			entries[index] = *entry
			send(StreamerUpdate{
				Index: index,
				Entry: entry,
			})
			syncRuntimeState(ctx, ircSyncer, minerSyncer, state, viewer, entries)
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

	watchStreak, err := service.WatchStreak(ctx, channel.Login)
	switch {
	case err == nil:
		entry.WatchStreak = watchStreak
	case errors.Is(err, context.Canceled):
		return nil, err
	default:
		entry.WatchStreakError = err.Error()
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

func syncRuntimeState(ctx context.Context, ircSyncer ircSyncer, minerSyncer minerSyncer, state *auth.State, viewer *twitch.Viewer, entries []twitch.StreamerEntry) {
	if ircSyncer != nil && state != nil {
		ircSyncer.Sync(ctx, state.Login, state.AccessToken, watchedIRCTargets(entries))
	}
	if minerSyncer != nil {
		minerSyncer.Sync(ctx, state, viewer, entries)
	}
}

func watchedIRCTargets(entries []twitch.StreamerEntry) []string {
	targets := make([]string, 0, 2)
	for _, entry := range entries {
		if entry.Status != twitch.StreamerReady || !entry.Live || entry.Login == "" {
			continue
		}
		targets = append(targets, entry.Login)
		if len(targets) == 2 {
			break
		}
	}
	return targets
}
