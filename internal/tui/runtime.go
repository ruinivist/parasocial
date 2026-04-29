package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
	AuthPath  string
	Input     io.Reader
	Output    io.Writer

	runtime          modelRuntime
	initialStreamers []twitch.StreamerEntry
	httpClient       *http.Client
}

type runtime struct {
	ctx          context.Context
	logins       []string
	httpClient   *http.Client
	authClient   *auth.Client
	authPath     string
	refreshEvery time.Duration
	sleep        func(context.Context, time.Duration) error
}

// Run starts the Bubble Tea UI and keeps application orchestration inside the TUI package.
func Run(ctx context.Context, options Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	rt := newRuntime(ctx, options)
	state := options.AuthState
	if state == nil {
		var err error
		state, err = rt.reuseAuth()
		if err != nil {
			return err
		}
	}

	options.AuthState = state
	options.runtime = rt

	input := options.Input
	if input == nil {
		input = os.Stdin
	}
	output := options.Output
	if output == nil {
		output = os.Stdout
	}

	program := tea.NewProgram(
		New(options),
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
	)
	_, err := program.Run()
	return err
}

func newRuntime(ctx context.Context, options Options) *runtime {
	httpClient := options.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	authPath := options.AuthPath
	if authPath == "" {
		authPath = auth.DefaultPath()
	}
	return &runtime{
		ctx:          ctx,
		logins:       append([]string(nil), options.Streamers...),
		httpClient:   httpClient,
		authClient:   auth.NewClient(httpClient),
		authPath:     authPath,
		refreshEvery: streamerRefreshInterval,
		sleep:        sleepContext,
	}
}

func (r *runtime) reuseAuth() (*auth.State, error) {
	return r.authClient.ReuseAuth(r.ctx, r.authPath)
}

func (r *runtime) startAuth(ch chan<- AuthUpdate) {
	go func() {
		defer close(ch)

		state, err := r.authClient.EnsureAuth(r.ctx, r.authPath, func(line string) {
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
		ircManager := newIRCManager(ch)
		defer ircManager.Close()

		service, err := newTwitchService(r.httpClient, state)
		if err != nil {
			ch <- StreamerUpdate{Err: err, Done: true}
			return
		}
		minerManager := miner.NewManager(r.ctx, service, nil)
		defer minerManager.Close()

		if err := resolveStreamerEntriesWithSleep(r.ctx, service, state, r.logins, ircManager, minerManager, func(update StreamerUpdate) {
			ch <- update
		}, r.refreshEvery, r.sleep); err != nil {
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
	PlaybackAccessToken(context.Context, string) (*twitch.PlaybackToken, error)
	LoadChannelPointsContext(context.Context, string) (*twitch.ChannelPointsContext, error)
	ClaimCommunityPoints(context.Context, string, string) error
	StreamMetadata(context.Context, string) (*twitch.StreamMetadata, error)
	FetchSpadeURL(context.Context, string) (string, error)
	TouchPlayback(context.Context, string, *twitch.PlaybackToken) error
	SendMinuteWatched(context.Context, string, twitch.MinuteWatchedPayload) error
}

type ircSyncer interface {
	Sync(context.Context, string, string, []irc.Target)
}

type minerSyncer interface {
	Sync(context.Context, *auth.State, *twitch.Viewer, []twitch.StreamerEntry)
}

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
	return &twitch.Service{
		GQL:        client,
		HTTPClient: httpClient,
		Session:    client.Session,
	}, nil
}

func newIRCManager(ch chan<- StreamerUpdate) *irc.Manager {
	return &irc.Manager{
		Addr: irc.DefaultAddr,
		Events: func(event irc.Event) {
			ch <- StreamerUpdate{
				IRC: &IRCUpdate{
					Login: event.Streamer,
					State: IRCState(event.State),
					Line:  event.Line,
				},
			}
		},
	}
}

func resolveStreamerEntries(ctx context.Context, service streamerService, state *auth.State, logins []string, ircSyncer ircSyncer, minerSyncer minerSyncer, send func(StreamerUpdate)) error {
	return resolveStreamerEntriesWithSleep(ctx, service, state, logins, ircSyncer, minerSyncer, send, streamerRefreshInterval, sleepContext)
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
