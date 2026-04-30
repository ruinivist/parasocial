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
const (
	maxWatchedChannels          = 2
	watchStreakMaintenanceLimit = 7 * time.Minute
	watchStreakRestartThreshold = 30 * time.Minute
	recentOfflineRestartGuard   = time.Minute
)

// Service is the Twitch capability surface the miner needs.
type Service interface {
	LoadChannelPointsContext(context.Context, string) (*twitch.ChannelPointsContext, error)
	WatchStreak(context.Context, string) (*int, error)
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

// WatchReason describes why the miner is currently watching a streamer.
type WatchReason string

const (
	WatchReasonStreak WatchReason = "watchstreak"
	WatchReasonPoints WatchReason = "points"
)

// StatusEntry is the current miner state for one resolved streamer login.
type StatusEntry struct {
	Login              string
	Watching           bool
	Reason             WatchReason
	WatchedMinutes     int
	WatchStreakMinutes int
	WatchStreak        *int
}

// Manager owns background channel points mining state for the current authenticated user.
type Manager struct {
	ctx           context.Context
	service       Service
	pubsub        PubSubSyncer
	onLog         func(LogEntry)
	onStatus      func(StatusEntry)
	watchInterval time.Duration
	sleep         func(context.Context, time.Duration) error
	now           func() time.Time

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

	WatchStreak        *int
	WatchStreakMissing bool
	WatchStreakWatched time.Duration
	Watched            time.Duration
	CurrentWatchReason WatchReason
	OnlineAt           time.Time
	OfflineAt          time.Time
	PendingStreamUpAt  time.Time
	CurrentBroadcastID string

	seeded     bool
	seeding    bool
	refreshing bool
}

type watchCandidate struct {
	configLogin string
	login       string
	channelID   string
	spadeURL    string
	metadata    *twitch.StreamMetadata
	reason      WatchReason
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
		now:           time.Now,
		entries:       make(map[string]*streamerState),
	}
	if manager.pubsub == nil {
		manager.pubsub = NewPubSubClient(ctx, manager.handlePubSubEvent)
	}
	return manager
}

// SetStatusSink wires a separate miner status callback used by the TUI.
func (m *Manager) SetStatusSink(onStatus func(StatusEntry)) {
	m.mu.Lock()
	m.onStatus = onStatus
	m.mu.Unlock()
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
		statuses   []StatusEntry
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
		current.WatchStreak = cloneInt(entry.WatchStreak)
		now := m.now()
		switch {
		case entry.Live && !current.Live:
			m.markOnlineConfirmedLocked(current, now, entry.WatchStreak)
			statuses = append(statuses, statusFromState(*current))
		case !entry.Live && current.Live:
			m.markOfflineLocked(current, now)
			statuses = append(statuses, statusFromState(*current))
		default:
			current.Live = entry.Live
		}
		nextEntries[entry.ConfigLogin] = current
	}

	m.entries = nextEntries
	m.startWatchLoopLocked()
	m.mu.Unlock()
	m.emitStatuses(statuses)

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
			current.CurrentBroadcastID = metadata.BroadcastID
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
	m.mu.Lock()
	viewer := m.viewer
	candidates := m.watchCandidatesLocked()
	statuses := m.updateWatchReasonsLocked(candidates)
	m.mu.Unlock()
	m.emitStatuses(statuses)

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
		statuses := []StatusEntry{}
		if state, ok := m.entries[candidate.configLogin]; ok && state.ChannelID == candidate.channelID {
			state.LastWatchAt = m.now()
			state.Watched += time.Minute
			if candidate.reason == WatchReasonStreak && state.WatchStreakMissing {
				state.WatchStreakWatched += time.Minute
				if state.WatchStreakWatched >= watchStreakMaintenanceLimit {
					state.WatchStreakMissing = false
					state.CurrentWatchReason = WatchReasonPoints
				}
			}
			statuses = append(statuses, statusFromState(*state))
		}
		m.mu.Unlock()
		m.emitStatuses(statuses)
	}
	return nil
}

func (m *Manager) watchCandidatesLocked() []watchCandidate {
	selected := make([]watchCandidate, 0, maxWatchedChannels)
	seen := make(map[string]bool)
	for _, configLogin := range m.order {
		if len(selected) == maxWatchedChannels {
			break
		}
		state, ok := m.entries[configLogin]
		if !ok || !state.eligibleForStreakWatch() {
			continue
		}
		selected = append(selected, watchCandidateFromState(*state, WatchReasonStreak))
		seen[configLogin] = true
	}

	for _, configLogin := range m.order {
		if len(selected) == maxWatchedChannels {
			break
		}
		if seen[configLogin] {
			continue
		}
		state, ok := m.entries[configLogin]
		if !ok || !state.eligibleForWatch() {
			continue
		}
		selected = append(selected, watchCandidateFromState(*state, WatchReasonPoints))
		seen[configLogin] = true
	}
	return selected
}

func watchCandidateFromState(state streamerState, reason WatchReason) watchCandidate {
	return watchCandidate{
		configLogin: state.ConfigLogin,
		login:       state.Login,
		channelID:   state.ChannelID,
		spadeURL:    state.SpadeURL,
		metadata:    state.Metadata,
		reason:      reason,
	}
}

func (s streamerState) eligibleForWatch() bool {
	return s.Live && s.Login != "" && s.ChannelID != ""
}

func (s streamerState) eligibleForStreakWatch() bool {
	if !s.eligibleForWatch() || !s.WatchStreakMissing || s.WatchStreakWatched >= watchStreakMaintenanceLimit {
		return false
	}
	if s.OfflineAt.IsZero() {
		return true
	}
	return !s.OnlineAt.IsZero() && s.OnlineAt.Sub(s.OfflineAt) > watchStreakRestartThreshold
}

func (m *Manager) updateWatchReasonsLocked(candidates []watchCandidate) []StatusEntry {
	reasons := make(map[string]WatchReason, len(candidates))
	for _, candidate := range candidates {
		reasons[candidate.configLogin] = candidate.reason
	}

	statuses := make([]StatusEntry, 0, len(m.entries))
	for configLogin, state := range m.entries {
		nextReason := reasons[configLogin]
		if !state.Live {
			nextReason = ""
		}
		if state.CurrentWatchReason == nextReason {
			continue
		}
		state.CurrentWatchReason = nextReason
		statuses = append(statuses, statusFromState(*state))
	}
	return statuses
}

func (m *Manager) markOnlineConfirmedLocked(state *streamerState, now time.Time, watchStreak *int) {
	state.Live = true
	state.PendingStreamUpAt = time.Time{}
	if watchStreak != nil {
		state.WatchStreak = cloneInt(watchStreak)
	}
	if !state.OfflineAt.IsZero() && now.Sub(state.OfflineAt) < recentOfflineRestartGuard {
		return
	}
	state.OnlineAt = now
	state.WatchStreakMissing = true
	state.WatchStreakWatched = 0
	state.SpadeURL = ""
	state.Metadata = nil
	state.CurrentBroadcastID = ""
}

func (m *Manager) markOfflineLocked(state *streamerState, now time.Time) {
	state.Live = false
	state.OfflineAt = now
	state.SpadeURL = ""
	state.Metadata = nil
	state.Watched = 0
	state.CurrentWatchReason = ""
	state.PendingStreamUpAt = time.Time{}
	state.CurrentBroadcastID = ""
}

func (m *Manager) emitStatusForConfig(configLogin string) {
	m.mu.Lock()
	var (
		status StatusEntry
		ok     bool
	)
	if state, found := m.entries[configLogin]; found {
		status = statusFromState(*state)
		ok = true
	}
	m.mu.Unlock()
	if ok {
		m.emitStatuses([]StatusEntry{status})
	}
}

func (m *Manager) emitStatuses(statuses []StatusEntry) {
	if len(statuses) == 0 {
		return
	}
	m.mu.Lock()
	onStatus := m.onStatus
	m.mu.Unlock()
	if onStatus == nil {
		return
	}
	for _, status := range statuses {
		onStatus(status)
	}
}

func statusFromState(state streamerState) StatusEntry {
	return StatusEntry{
		Login:              state.Login,
		Watching:           state.Live && state.CurrentWatchReason != "",
		Reason:             state.CurrentWatchReason,
		WatchedMinutes:     int(state.Watched / time.Minute),
		WatchStreakMinutes: int(state.WatchStreakWatched / time.Minute),
		WatchStreak:        cloneInt(state.WatchStreak),
	}
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
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
			if event.ReasonCode == "WATCH_STREAK" {
				current.WatchStreakMissing = false
				if current.CurrentWatchReason == WatchReasonStreak {
					current.CurrentWatchReason = WatchReasonPoints
				}
			}
		}
		m.mu.Unlock()
		if event.ReasonCode == "WATCH_STREAK" {
			m.emitStatusForConfig(configLogin)
			m.scheduleWatchStreakRefresh(configLogin)
		}
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
			current.PendingStreamUpAt = m.now()
		}
		m.mu.Unlock()
	case "stream-down":
		var statuses []StatusEntry
		m.mu.Lock()
		if current, ok := m.entries[configLogin]; ok {
			m.markOfflineLocked(current, m.now())
			statuses = append(statuses, statusFromState(*current))
		}
		m.mu.Unlock()
		m.emitStatuses(statuses)
	case "viewcount":
		if !state.Live {
			var (
				statuses      []StatusEntry
				shouldRefresh bool
			)
			m.mu.Lock()
			if current, ok := m.entries[configLogin]; ok {
				m.markOnlineConfirmedLocked(current, m.now(), nil)
				statuses = append(statuses, statusFromState(*current))
				shouldRefresh = current.Live
			}
			m.mu.Unlock()
			m.emitStatuses(statuses)
			if shouldRefresh {
				m.scheduleRefresh(configLogin)
				m.scheduleWatchStreakRefresh(configLogin)
			}
		}
	}
}

func (m *Manager) scheduleWatchStreakRefresh(configLogin string) {
	m.mu.Lock()
	state, ok := m.entries[configLogin]
	if !ok {
		m.mu.Unlock()
		return
	}
	login := state.Login
	channelID := state.ChannelID
	m.mu.Unlock()

	go func() {
		streak, err := m.service.WatchStreak(m.ctx, login)
		if err != nil {
			m.logf(login, "watch streak refresh failed: %v", err)
			return
		}
		m.mu.Lock()
		if current, ok := m.entries[configLogin]; ok && current.ChannelID == channelID {
			current.WatchStreak = cloneInt(streak)
		}
		m.mu.Unlock()
		m.emitStatusForConfig(configLogin)
	}()
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
