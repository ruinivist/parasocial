package miner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"parasocial/internal/auth"
	"parasocial/internal/twitch"
)

const defaultWatchInterval = 20 * time.Second

// Service is the Twitch capability surface the miner needs.
type Service interface {
	LoadChannelPointsContext(context.Context, string) (*twitch.ChannelPointsContext, error)
	ClaimCommunityPoints(context.Context, string, string) error
	StreamMetadata(context.Context, string) (*twitch.StreamMetadata, error)
	FetchSpadeURL(context.Context, string) (string, error)
	PlaybackAccessToken(context.Context, string) (*twitch.PlaybackToken, error)
	TouchPlayback(context.Context, string, *twitch.PlaybackToken) error
	SendMinuteWatched(context.Context, string, twitch.MinuteWatchedPayload) error
}

// PubSubSyncer reconciles the live PubSub subscriptions the miner needs.
type PubSubSyncer interface {
	Sync(context.Context, string, string, []string) error
	Close() error
}

// LogEntry is one miner log line associated with a resolved streamer login.
type LogEntry struct {
	Login string
	Line  string
}

// Manager owns background channel points mining state for the current authenticated user.
type Manager struct {
	ctx           context.Context
	service       Service
	pubsub        PubSubSyncer
	onLog         func(LogEntry)
	watchInterval time.Duration
	sleep         func(context.Context, time.Duration) error

	mu      sync.Mutex
	auth    *auth.State
	viewer  *twitch.Viewer
	order   []string
	entries map[string]*streamerState

	watchStarted bool
	watchCancel  context.CancelFunc
}

type streamerState struct {
	ConfigLogin   string
	Login         string
	ChannelID     string
	Live          bool
	ChannelPoints int
	SpadeURL      string
	Metadata      *twitch.StreamMetadata
	LastWatchAt   time.Time

	seeded     bool
	seeding    bool
	refreshing bool
}

// NewManager constructs one background miner manager.
func NewManager(ctx context.Context, service Service, pubsub PubSubSyncer, onLog func(LogEntry)) *Manager {
	manager := &Manager{
		ctx:           ctx,
		service:       service,
		pubsub:        pubsub,
		onLog:         onLog,
		watchInterval: defaultWatchInterval,
		sleep:         sleepContext,
		entries:       make(map[string]*streamerState),
	}
	if manager.pubsub == nil {
		manager.pubsub = NewPubSubClient(ctx, manager.handlePubSubEvent)
	}
	return manager
}

// Sync reconciles the miner against the current resolved streamer snapshot.
func (m *Manager) Sync(ctx context.Context, state *auth.State, viewer *twitch.Viewer, entries []twitch.StreamerEntry) {
	if state == nil || viewer == nil {
		return
	}

	type seedTarget struct {
		configLogin string
	}

	var (
		channelIDs []string
		seedList   []seedTarget
	)

	m.mu.Lock()
	m.auth = state
	m.viewer = viewer
	m.order = m.order[:0]

	nextEntries := make(map[string]*streamerState, len(entries))
	for _, entry := range entries {
		m.order = append(m.order, entry.ConfigLogin)
		if entry.Status != twitch.StreamerReady || entry.ChannelID == "" || entry.Login == "" {
			continue
		}

		channelIDs = append(channelIDs, entry.ChannelID)
		current, ok := m.entries[entry.ConfigLogin]
		if !ok || current.ChannelID != entry.ChannelID || current.Login != entry.Login {
			current = &streamerState{
				ConfigLogin: entry.ConfigLogin,
				Login:       entry.Login,
				ChannelID:   entry.ChannelID,
			}
			seedList = append(seedList, seedTarget{configLogin: entry.ConfigLogin})
		}

		current.ConfigLogin = entry.ConfigLogin
		current.Login = entry.Login
		current.ChannelID = entry.ChannelID
		current.Live = entry.Live
		nextEntries[entry.ConfigLogin] = current
	}

	m.entries = nextEntries
	m.startWatchLoopLocked()
	m.mu.Unlock()

	_ = m.pubsub.Sync(ctx, viewer.ID, state.AccessToken, channelIDs)
	for _, target := range seedList {
		m.scheduleSeed(target.configLogin)
	}
}

// Close stops background work and closes the PubSub connection.
func (m *Manager) Close() {
	m.mu.Lock()
	cancel := m.watchCancel
	m.watchCancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if m.pubsub != nil {
		_ = m.pubsub.Close()
	}
}

func (m *Manager) startWatchLoopLocked() {
	if m.watchStarted {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.watchStarted = true
	m.watchCancel = cancel
	go m.watchLoop(ctx)
}

func (m *Manager) scheduleSeed(configLogin string) {
	m.mu.Lock()
	state, ok := m.entries[configLogin]
	if !ok || state.seeding {
		m.mu.Unlock()
		return
	}
	state.seeding = true
	login := state.Login
	channelID := state.ChannelID
	live := state.Live
	m.mu.Unlock()

	go func() {
		defer m.finishSeeding(configLogin)

		channelPoints, err := m.service.LoadChannelPointsContext(m.ctx, login)
		if err != nil {
			m.logf(login, "channel points seed failed: %v", err)
		} else {
			m.mu.Lock()
			if current, ok := m.entries[configLogin]; ok && current.ChannelID == channelID {
				current.ChannelPoints = channelPoints.Balance
				current.seeded = true
			}
			m.mu.Unlock()
			m.logf(login, "seeded channel points balance: %d", channelPoints.Balance)
			if channelPoints.ClaimID != "" {
				m.logf(login, "claiming bonus chest: %s", channelPoints.ClaimID)
				if err := m.service.ClaimCommunityPoints(m.ctx, channelID, channelPoints.ClaimID); err != nil {
					m.logf(login, "claim failed: %v", err)
				}
			}
		}

		if live {
			m.scheduleRefresh(configLogin)
		}
	}()
}

func (m *Manager) finishSeeding(configLogin string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state, ok := m.entries[configLogin]; ok {
		state.seeding = false
	}
}

func (m *Manager) scheduleRefresh(configLogin string) {
	m.mu.Lock()
	state, ok := m.entries[configLogin]
	if !ok || state.refreshing {
		m.mu.Unlock()
		return
	}
	state.refreshing = true
	login := state.Login
	channelID := state.ChannelID
	m.mu.Unlock()

	go func() {
		defer m.finishRefresh(configLogin)

		spadeURL, err := m.service.FetchSpadeURL(m.ctx, login)
		if err != nil {
			m.logf(login, "metadata refresh failed: fetch spade url: %v", err)
			return
		}
		metadata, err := m.service.StreamMetadata(m.ctx, login)
		if err != nil {
			m.logf(login, "metadata refresh failed: stream metadata: %v", err)
			return
		}

		m.mu.Lock()
		if current, ok := m.entries[configLogin]; ok && current.ChannelID == channelID {
			current.SpadeURL = spadeURL
			current.Metadata = metadata
			current.Live = true
		}
		m.mu.Unlock()
		m.logf(login, "refreshed playback metadata")
	}()
}

func (m *Manager) finishRefresh(configLogin string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state, ok := m.entries[configLogin]; ok {
		state.refreshing = false
	}
}

func (m *Manager) watchLoop(ctx context.Context) {
	for {
		if err := m.watchOnce(ctx); err != nil {
			if err == context.Canceled {
				return
			}
		}
		if err := m.sleep(ctx, m.watchInterval); err != nil {
			return
		}
	}
}

func (m *Manager) watchOnce(ctx context.Context) error {
	type candidate struct {
		configLogin string
		login       string
		channelID   string
		spadeURL    string
		metadata    *twitch.StreamMetadata
	}

	m.mu.Lock()
	viewer := m.viewer
	candidates := make([]candidate, 0, 2)
	for _, configLogin := range m.order {
		state, ok := m.entries[configLogin]
		if !ok || !state.Live || state.Login == "" || state.ChannelID == "" {
			continue
		}
		candidates = append(candidates, candidate{
			configLogin: configLogin,
			login:       state.Login,
			channelID:   state.ChannelID,
			spadeURL:    state.SpadeURL,
			metadata:    state.Metadata,
		})
		if len(candidates) == 2 {
			break
		}
	}
	m.mu.Unlock()

	if viewer == nil || len(candidates) == 0 {
		return nil
	}

	for _, candidate := range candidates {
		if candidate.spadeURL == "" || candidate.metadata == nil {
			m.scheduleRefresh(candidate.configLogin)
			continue
		}

		token, err := m.service.PlaybackAccessToken(ctx, candidate.login)
		if err != nil {
			m.logf(candidate.login, "watch failed: playback token: %v", err)
			continue
		}
		if err := m.service.TouchPlayback(ctx, candidate.login, token); err != nil {
			m.logf(candidate.login, "watch failed: touch playback: %v", err)
			continue
		}

		payload := twitch.BuildMinuteWatchedPayload(viewer.ID, candidate.channelID, candidate.login, candidate.metadata)
		if err := m.service.SendMinuteWatched(ctx, candidate.spadeURL, payload); err != nil {
			m.logf(candidate.login, "watch failed: minute watched: %v", err)
			continue
		}

		m.mu.Lock()
		if state, ok := m.entries[candidate.configLogin]; ok && state.ChannelID == candidate.channelID {
			state.LastWatchAt = time.Now()
		}
		m.mu.Unlock()
	}
	return nil
}

func (m *Manager) handlePubSubEvent(event Event) {
	configLogin, state := m.streamerForChannel(event.ChannelID)
	if state == nil {
		return
	}

	m.logEvent(state.Login, event)

	switch event.MessageType {
	case "points-earned", "points-spent":
		m.mu.Lock()
		if current, ok := m.entries[configLogin]; ok {
			current.ChannelPoints = event.Balance
		}
		m.mu.Unlock()
	case "claim-available":
		if event.ClaimID != "" {
			m.logf(state.Login, "claiming bonus chest: %s", event.ClaimID)
			if err := m.service.ClaimCommunityPoints(m.ctx, state.ChannelID, event.ClaimID); err != nil {
				m.logf(state.Login, "claim failed: %v", err)
			}
		}
	case "stream-up":
		m.mu.Lock()
		if current, ok := m.entries[configLogin]; ok {
			current.Live = true
		}
		m.mu.Unlock()
		m.scheduleRefresh(configLogin)
	case "stream-down":
		m.mu.Lock()
		if current, ok := m.entries[configLogin]; ok {
			current.Live = false
		}
		m.mu.Unlock()
	case "viewcount":
		if !state.Live {
			m.mu.Lock()
			if current, ok := m.entries[configLogin]; ok {
				current.Live = true
			}
			m.mu.Unlock()
			m.scheduleRefresh(configLogin)
		}
	}
}

func (m *Manager) logEvent(login string, event Event) {
	switch event.MessageType {
	case "points-earned":
		m.logf(login, "pubsub points earned: balance=%d", event.Balance)
	case "points-spent":
		m.logf(login, "pubsub points spent: balance=%d", event.Balance)
	case "claim-available":
		if event.ClaimID != "" {
			m.logf(login, "pubsub claim available: %s", event.ClaimID)
			return
		}
		m.logf(login, "pubsub claim available")
	case "stream-up":
		m.logf(login, "pubsub stream up")
	case "stream-down":
		m.logf(login, "pubsub stream down")
	case "viewcount":
		m.logf(login, "pubsub viewcount heartbeat")
	default:
		m.logf(login, "pubsub %s", event.MessageType)
	}
}

func (m *Manager) logf(login, format string, args ...any) {
	if m.onLog == nil || login == "" {
		return
	}
	m.onLog(LogEntry{
		Login: login,
		Line:  fmt.Sprintf(format, args...),
	})
}

func (m *Manager) streamerForChannel(channelID string) (string, *streamerState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for configLogin, state := range m.entries {
		if state.ChannelID == channelID {
			copy := *state
			return configLogin, &copy
		}
	}
	return "", nil
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
