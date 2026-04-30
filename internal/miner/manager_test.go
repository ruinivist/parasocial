package miner

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"parasocial/internal/auth"
	"parasocial/internal/twitch"
)

type fakeService struct {
	channelPoints map[string]*twitch.ChannelPointsContext
	metadata      map[string]*twitch.StreamMetadata
	spadeURLs     map[string]string
	watchStreaks  map[string]int
	playbackErr   map[string]error
	minuteErr     map[string]error
	claimErr      map[string]error

	claimed   []string
	watched   []string
	refreshed []string
}

func (f *fakeService) LoadChannelPointsContext(_ context.Context, login string) (*twitch.ChannelPointsContext, error) {
	if result, ok := f.channelPoints[login]; ok {
		return result, nil
	}
	return nil, errors.New("missing channel points")
}

func (f *fakeService) WatchStreak(_ context.Context, login string) (*int, error) {
	if f.watchStreaks != nil {
		if value, ok := f.watchStreaks[login]; ok {
			return &value, nil
		}
	}
	value := 0
	return &value, nil
}

func (f *fakeService) ClaimCommunityPoints(_ context.Context, channelID, claimID string) error {
	f.claimed = append(f.claimed, channelID+":"+claimID)
	if err, ok := f.claimErr[channelID+":"+claimID]; ok {
		return err
	}
	return nil
}

func (f *fakeService) StreamMetadata(_ context.Context, login string) (*twitch.StreamMetadata, error) {
	f.refreshed = append(f.refreshed, login)
	if result, ok := f.metadata[login]; ok {
		return result, nil
	}
	return nil, errors.New("missing metadata")
}

func (f *fakeService) FetchSpadeURL(_ context.Context, login string) (string, error) {
	if result, ok := f.spadeURLs[login]; ok {
		return result, nil
	}
	return "", errors.New("missing spade url")
}

func (f *fakeService) PlaybackAccessToken(_ context.Context, login string) (*twitch.PlaybackToken, error) {
	if err, ok := f.playbackErr[login]; ok {
		return nil, err
	}
	return &twitch.PlaybackToken{Signature: "sig", Value: "token"}, nil
}

func (f *fakeService) TouchPlayback(_ context.Context, login string, _ *twitch.PlaybackToken) error {
	f.watched = append(f.watched, "touch:"+login)
	return nil
}

func (f *fakeService) SendMinuteWatched(_ context.Context, spadeURL string, _ twitch.MinuteWatchedPayload) error {
	f.watched = append(f.watched, "post:"+spadeURL)
	if err, ok := f.minuteErr[spadeURL]; ok {
		return err
	}
	return nil
}

type fakePubSub struct {
	syncCalls [][]string
}

func (f *fakePubSub) Sync(_ context.Context, _, _ string, channelIDs []string) error {
	f.syncCalls = append(f.syncCalls, append([]string(nil), channelIDs...))
	return nil
}

func (f *fakePubSub) Close() error { return nil }

func TestManagerSyncSeedsAndClaims(t *testing.T) {
	service := &fakeService{
		channelPoints: map[string]*twitch.ChannelPointsContext{
			"alpha_live": {Balance: 250, ClaimID: "claim-1"},
		},
		metadata: map[string]*twitch.StreamMetadata{
			"alpha_live": {BroadcastID: "broadcast"},
		},
		spadeURLs: map[string]string{
			"alpha_live": "https://spade.test/alpha",
		},
	}
	pubsub := &fakePubSub{}
	manager := NewManager(context.Background(), service, pubsub, nil)
	defer manager.Close()
	manager.sleep = cancelSleep

	manager.Sync(context.Background(), &auth.State{AccessToken: "token"}, &twitch.Viewer{ID: "viewer"}, []twitch.StreamerEntry{
		{ConfigLogin: "alpha", Login: "alpha_live", ChannelID: "1", Live: true, Status: twitch.StreamerReady},
	})

	waitFor(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(service.claimed) == 1 && manager.entries["alpha"].ChannelPoints == 250 && manager.entries["alpha"].SpadeURL != ""
	})

	if got, want := pubsub.syncCalls, [][]string{{"1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("syncCalls = %#v, want %#v", got, want)
	}
}

func TestManagerWatchOnceUsesTopTwoLiveStreamers(t *testing.T) {
	service := &fakeService{
		metadata: map[string]*twitch.StreamMetadata{
			"alpha_live": {BroadcastID: "a"},
			"beta_live":  {BroadcastID: "b"},
		},
		spadeURLs: map[string]string{
			"alpha_live": "https://spade.test/alpha",
			"beta_live":  "https://spade.test/beta",
		},
		channelPoints: map[string]*twitch.ChannelPointsContext{
			"alpha_live": {Balance: 10},
			"beta_live":  {Balance: 20},
			"gamma_live": {Balance: 30},
		},
	}
	manager := NewManager(context.Background(), service, &fakePubSub{}, nil)
	defer manager.Close()
	manager.sleep = cancelSleep

	manager.Sync(context.Background(), &auth.State{AccessToken: "token"}, &twitch.Viewer{ID: "viewer"}, []twitch.StreamerEntry{
		{ConfigLogin: "alpha", Login: "alpha_live", ChannelID: "1", Live: true, Status: twitch.StreamerReady},
		{ConfigLogin: "beta", Login: "beta_live", ChannelID: "2", Live: true, Status: twitch.StreamerReady},
		{ConfigLogin: "gamma", Login: "gamma_live", ChannelID: "3", Live: true, Status: twitch.StreamerReady},
	})

	waitFor(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return manager.entries["alpha"].SpadeURL != "" && manager.entries["beta"].SpadeURL != ""
	})

	if err := manager.watchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := service.watched, []string{
		"touch:alpha_live",
		"post:https://spade.test/alpha",
		"touch:beta_live",
		"post:https://spade.test/beta",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("watched = %#v, want %#v", got, want)
	}
}

func TestManagerWatchOncePrioritizesMissingWatchStreaks(t *testing.T) {
	service := readyWatchService("alpha_live", "beta_live", "gamma_live")
	manager := NewManager(context.Background(), service, &fakePubSub{}, nil)
	defer manager.Close()
	manager.viewer = &twitch.Viewer{ID: "viewer"}
	manager.order = []string{"alpha", "beta", "gamma"}
	manager.entries = map[string]*streamerState{
		"alpha": readyState("alpha", "alpha_live", "1", false),
		"beta":  readyState("beta", "beta_live", "2", true),
		"gamma": readyState("gamma", "gamma_live", "3", true),
	}

	if err := manager.watchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got, want := service.watched, []string{
		"touch:beta_live",
		"post:https://spade.test/beta_live",
		"touch:gamma_live",
		"post:https://spade.test/gamma_live",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("watched = %#v, want %#v", got, want)
	}
}

func TestManagerWatchOnceFillsWithPointsCandidates(t *testing.T) {
	service := readyWatchService("alpha_live", "beta_live", "gamma_live")
	manager := NewManager(context.Background(), service, &fakePubSub{}, nil)
	defer manager.Close()
	manager.viewer = &twitch.Viewer{ID: "viewer"}
	manager.order = []string{"alpha", "beta", "gamma"}
	manager.entries = map[string]*streamerState{
		"alpha": readyState("alpha", "alpha_live", "1", false),
		"beta":  readyState("beta", "beta_live", "2", true),
		"gamma": readyState("gamma", "gamma_live", "3", false),
	}

	if err := manager.watchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got, want := service.watched, []string{
		"touch:beta_live",
		"post:https://spade.test/beta_live",
		"touch:alpha_live",
		"post:https://spade.test/alpha_live",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("watched = %#v, want %#v", got, want)
	}
	if manager.entries["beta"].CurrentWatchReason != WatchReasonStreak {
		t.Fatalf("beta reason = %q, want streak", manager.entries["beta"].CurrentWatchReason)
	}
	if manager.entries["alpha"].CurrentWatchReason != WatchReasonPoints {
		t.Fatalf("alpha reason = %q, want points", manager.entries["alpha"].CurrentWatchReason)
	}
}

func TestManagerStopsWatchStreakMaintenanceAfterSevenMinutes(t *testing.T) {
	service := readyWatchService("alpha_live")
	manager := NewManager(context.Background(), service, &fakePubSub{}, nil)
	defer manager.Close()
	manager.viewer = &twitch.Viewer{ID: "viewer"}
	manager.order = []string{"alpha"}
	manager.entries = map[string]*streamerState{
		"alpha": readyState("alpha", "alpha_live", "1", true),
	}

	for i := 0; i < 7; i++ {
		if err := manager.watchOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	state := manager.entries["alpha"]
	if state.WatchStreakMissing {
		t.Fatal("expected watch streak maintenance to stop")
	}
	if state.WatchStreakWatched != watchStreakMaintenanceLimit {
		t.Fatalf("watched streak duration = %s, want %s", state.WatchStreakWatched, watchStreakMaintenanceLimit)
	}
	if state.Watched != watchStreakMaintenanceLimit {
		t.Fatalf("watched duration = %s, want %s", state.Watched, watchStreakMaintenanceLimit)
	}
	if state.CurrentWatchReason != WatchReasonPoints {
		t.Fatalf("watch reason = %q, want points", state.CurrentWatchReason)
	}
}

func TestManagerTracksSuccessfulWatchedMinutesAndResetsOffline(t *testing.T) {
	service := readyWatchService("alpha_live")
	manager := NewManager(context.Background(), service, &fakePubSub{}, nil)
	defer manager.Close()
	manager.viewer = &twitch.Viewer{ID: "viewer"}
	manager.order = []string{"alpha"}
	manager.entries = map[string]*streamerState{
		"alpha": readyState("alpha", "alpha_live", "1", false),
	}

	if err := manager.watchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.watchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	state := manager.entries["alpha"]
	if state.Watched != 2*time.Minute {
		t.Fatalf("watched duration = %s, want 2m", state.Watched)
	}
	status := statusFromState(*state)
	if status.WatchedMinutes != 2 {
		t.Fatalf("status watched minutes = %d, want 2", status.WatchedMinutes)
	}

	manager.handlePubSubEvent(Event{MessageType: "stream-down", ChannelID: "1", Timestamp: "down", Topic: "video-playback-by-id"})

	state = manager.entries["alpha"]
	if state.Watched != 0 {
		t.Fatalf("watched duration after offline = %s, want 0", state.Watched)
	}
	status = statusFromState(*state)
	if status.Watching || status.WatchedMinutes != 0 {
		t.Fatalf("status after offline = %#v, want not watching with 0 watched minutes", status)
	}
}

func TestManagerDoesNotTrackWatchedMinutesWhenTelemetryFails(t *testing.T) {
	service := readyWatchService("alpha_live")
	service.minuteErr = map[string]error{
		"https://spade.test/alpha_live": errors.New("minute rejected"),
	}
	manager := NewManager(context.Background(), service, &fakePubSub{}, nil)
	defer manager.Close()
	manager.viewer = &twitch.Viewer{ID: "viewer"}
	manager.order = []string{"alpha"}
	manager.entries = map[string]*streamerState{
		"alpha": readyState("alpha", "alpha_live", "1", false),
	}

	if err := manager.watchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if state := manager.entries["alpha"]; state.Watched != 0 {
		t.Fatalf("watched duration = %s, want 0 after failed telemetry", state.Watched)
	}
}

func TestHandlePubSubWatchStreakCompletesMaintenance(t *testing.T) {
	service := &fakeService{watchStreaks: map[string]int{"alpha_live": 8}}
	manager := NewManager(context.Background(), service, &fakePubSub{}, nil)
	defer manager.Close()
	manager.entries["alpha"] = &streamerState{
		ConfigLogin:        "alpha",
		ChannelID:          "1",
		Login:              "alpha_live",
		Live:               true,
		WatchStreakMissing: true,
		CurrentWatchReason: WatchReasonStreak,
	}

	manager.handlePubSubEvent(Event{MessageType: "points-earned", ChannelID: "1", Balance: 555, ReasonCode: "WATCH_STREAK", TotalPoints: 450, Timestamp: "t1", Topic: "community-points-user-v1"})

	waitFor(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		state := manager.entries["alpha"]
		return !state.WatchStreakMissing && state.CurrentWatchReason == WatchReasonPoints && state.WatchStreak != nil && *state.WatchStreak == 8
	})
}

func TestManagerLifecycleResetsWatchStreakOnConfirmedOnline(t *testing.T) {
	service := &fakeService{}
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	manager := NewManager(context.Background(), service, &fakePubSub{}, nil)
	defer manager.Close()
	manager.now = func() time.Time { return now }
	manager.entries["alpha"] = &streamerState{
		ConfigLogin:        "alpha",
		ChannelID:          "1",
		Login:              "alpha_live",
		Live:               true,
		WatchStreakMissing: false,
		WatchStreakWatched: 5 * time.Minute,
		SpadeURL:           "https://spade.test/alpha",
		Metadata:           &twitch.StreamMetadata{BroadcastID: "old"},
	}

	manager.handlePubSubEvent(Event{MessageType: "stream-down", ChannelID: "1", Timestamp: "down", Topic: "video-playback-by-id"})
	now = now.Add(31 * time.Minute)
	manager.handlePubSubEvent(Event{MessageType: "viewcount", ChannelID: "1", Timestamp: "viewcount", Topic: "video-playback-by-id"})

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.entries["alpha"]
	if !state.Live || !state.WatchStreakMissing || state.WatchStreakWatched != 0 {
		t.Fatalf("state = %#v, want fresh live streak maintenance", state)
	}
	if state.SpadeURL != "" || state.Metadata != nil {
		t.Fatalf("playback metadata was not cleared: %#v", state)
	}
}

func TestManagerLifecycleRecentOfflineGuardPreservesStreakState(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	manager := NewManager(context.Background(), &fakeService{}, &fakePubSub{}, nil)
	defer manager.Close()
	manager.now = func() time.Time { return now }
	manager.entries["alpha"] = &streamerState{
		ConfigLogin:        "alpha",
		ChannelID:          "1",
		Login:              "alpha_live",
		Live:               false,
		OfflineAt:          now.Add(-30 * time.Second),
		WatchStreakMissing: false,
		WatchStreakWatched: 5 * time.Minute,
	}

	manager.handlePubSubEvent(Event{MessageType: "viewcount", ChannelID: "1", Timestamp: "viewcount", Topic: "video-playback-by-id"})

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.entries["alpha"]
	if !state.Live {
		t.Fatal("expected channel to be live after viewcount")
	}
	if state.WatchStreakMissing || state.WatchStreakWatched != 5*time.Minute {
		t.Fatalf("state = %#v, want recent offline guard to preserve streak state", state)
	}
}

func TestManagerSyncPollingConfirmedOnlineResetsStreakState(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	manager := NewManager(context.Background(), &fakeService{}, &fakePubSub{}, nil)
	defer manager.Close()
	manager.sleep = cancelSleep
	manager.now = func() time.Time { return now }
	manager.entries["alpha"] = &streamerState{
		ConfigLogin:        "alpha",
		ChannelID:          "1",
		Login:              "alpha_live",
		Live:               false,
		OfflineAt:          now.Add(-31 * time.Minute),
		WatchStreakMissing: false,
		WatchStreakWatched: 5 * time.Minute,
	}
	streak := 9

	manager.Sync(context.Background(), &auth.State{AccessToken: "token"}, &twitch.Viewer{ID: "viewer"}, []twitch.StreamerEntry{
		{ConfigLogin: "alpha", Login: "alpha_live", ChannelID: "1", Live: true, Status: twitch.StreamerReady, WatchStreak: &streak},
	})

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.entries["alpha"]
	if !state.Live || !state.WatchStreakMissing || state.WatchStreakWatched != 0 || state.WatchStreak == nil || *state.WatchStreak != 9 {
		t.Fatalf("state = %#v, want polling-confirmed fresh live streak state", state)
	}
}

func TestManagerStreamUpWaitsForConfirmation(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	manager := NewManager(context.Background(), &fakeService{}, &fakePubSub{}, nil)
	defer manager.Close()
	manager.now = func() time.Time { return now }
	manager.entries["alpha"] = &streamerState{ConfigLogin: "alpha", ChannelID: "1", Login: "alpha_live"}

	manager.handlePubSubEvent(Event{MessageType: "stream-up", ChannelID: "1", Timestamp: "up", Topic: "video-playback-by-id"})

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.entries["alpha"]
	if state.Live {
		t.Fatal("stream-up should wait for API/viewcount confirmation")
	}
	if !state.PendingStreamUpAt.Equal(now) {
		t.Fatalf("pending stream-up = %s, want %s", state.PendingStreamUpAt, now)
	}
}

func TestHandlePubSubEventUpdatesBalanceAndClaims(t *testing.T) {
	service := &fakeService{}
	manager := NewManager(context.Background(), service, &fakePubSub{}, nil)
	defer manager.Close()
	manager.entries["alpha"] = &streamerState{ConfigLogin: "alpha", ChannelID: "1", Login: "alpha_live", Live: true}

	manager.handlePubSubEvent(Event{MessageType: "points-earned", ChannelID: "1", Balance: 555, Timestamp: "t1", Topic: "community-points-user-v1"})
	manager.handlePubSubEvent(Event{MessageType: "claim-available", ChannelID: "1", ClaimID: "claim-2", Timestamp: "t2", Topic: "community-points-user-v1"})

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.entries["alpha"].ChannelPoints != 555 {
		t.Fatalf("channelPoints = %d", manager.entries["alpha"].ChannelPoints)
	}
	if got, want := service.claimed, []string{"1:claim-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claimed = %#v, want %#v", got, want)
	}
}

func TestManagerLogsPubSubEventsAndFailures(t *testing.T) {
	service := &fakeService{
		claimErr: map[string]error{
			"1:claim-2": errors.New("claim rejected"),
		},
	}
	var logs []LogEntry
	manager := NewManager(context.Background(), service, &fakePubSub{}, func(entry LogEntry) {
		logs = append(logs, entry)
	})
	defer manager.Close()
	manager.entries["alpha"] = &streamerState{ConfigLogin: "alpha", ChannelID: "1", Login: "alpha_live", Live: true}

	manager.handlePubSubEvent(Event{MessageType: "points-earned", ChannelID: "1", Balance: 555, Timestamp: "t1", Topic: "community-points-user-v1"})
	manager.handlePubSubEvent(Event{MessageType: "claim-available", ChannelID: "1", ClaimID: "claim-2", Timestamp: "t2", Topic: "community-points-user-v1"})

	if got, want := logs, []LogEntry{
		{Login: "alpha_live", Line: "pubsub points earned: balance=555"},
		{Login: "alpha_live", Line: "pubsub claim available: claim-2"},
		{Login: "alpha_live", Line: "claiming bonus chest: claim-2"},
		{Login: "alpha_live", Line: "claim failed: claim rejected"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("logs = %#v, want %#v", got, want)
	}
}

func cancelSleep(context.Context, time.Duration) error { return context.Canceled }

func readyWatchService(logins ...string) *fakeService {
	service := &fakeService{
		metadata:  map[string]*twitch.StreamMetadata{},
		spadeURLs: map[string]string{},
	}
	for _, login := range logins {
		service.metadata[login] = &twitch.StreamMetadata{BroadcastID: "broadcast-" + login}
		service.spadeURLs[login] = "https://spade.test/" + login
	}
	return service
}

func readyState(configLogin, login, channelID string, missingStreak bool) *streamerState {
	return &streamerState{
		ConfigLogin:        configLogin,
		Login:              login,
		ChannelID:          channelID,
		Live:               true,
		SpadeURL:           "https://spade.test/" + login,
		Metadata:           &twitch.StreamMetadata{BroadcastID: "broadcast-" + login},
		WatchStreakMissing: missingStreak,
		OnlineAt:           time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
