package tui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"parasocial/internal/auth"
	"parasocial/internal/irc"
	"parasocial/internal/miner"
	"parasocial/internal/twitch"
)

type fakeStreamerService struct {
	channels    map[string]*twitch.Channel
	resolveErrs map[string]error
	streams     map[string][]streamResult
	streamCalls map[string]int
	playbackErr map[string]error
}

type streamResult struct {
	info *twitch.StreamInfo
	err  error
}

func (f *fakeStreamerService) CurrentUser(context.Context) (*twitch.Viewer, error) {
	return &twitch.Viewer{ID: "7", Login: "viewer"}, nil
}

func (f *fakeStreamerService) ResolveStreamer(_ context.Context, login string) (*twitch.Channel, error) {
	if err, ok := f.resolveErrs[login]; ok {
		return nil, err
	}
	return f.channels[login], nil
}

func (f *fakeStreamerService) StreamInfo(_ context.Context, channelID string) (*twitch.StreamInfo, error) {
	call := f.streamCalls[channelID]
	f.streamCalls[channelID] = call + 1
	results := f.streams[channelID]
	if len(results) == 0 {
		return &twitch.StreamInfo{Online: false}, nil
	}
	if call >= len(results) {
		call = len(results) - 1
	}
	if results[call].err != nil {
		return nil, results[call].err
	}
	return results[call].info, nil
}

func (f *fakeStreamerService) PlaybackAccessToken(_ context.Context, login string) (*twitch.PlaybackToken, error) {
	if err, ok := f.playbackErr[login]; ok {
		return nil, err
	}
	return &twitch.PlaybackToken{Signature: "sig", Value: "token"}, nil
}

func (f *fakeStreamerService) LoadChannelPointsContext(context.Context, string) (*twitch.ChannelPointsContext, error) {
	return &twitch.ChannelPointsContext{Balance: 0}, nil
}

func (f *fakeStreamerService) ClaimCommunityPoints(context.Context, string, string) error {
	return nil
}

func (f *fakeStreamerService) StreamMetadata(context.Context, string) (*twitch.StreamMetadata, error) {
	return &twitch.StreamMetadata{BroadcastID: "broadcast"}, nil
}

func (f *fakeStreamerService) FetchSpadeURL(context.Context, string) (string, error) {
	return "https://spade.test", nil
}

func (f *fakeStreamerService) TouchPlayback(context.Context, string, *twitch.PlaybackToken) error {
	return nil
}

func (f *fakeStreamerService) SendMinuteWatched(context.Context, string, twitch.MinuteWatchedPayload) error {
	return nil
}

type fakeIRCSyncer struct {
	calls [][]string
}

func (f *fakeIRCSyncer) Sync(_ context.Context, _, _ string, targets []irc.Target) {
	logins := make([]string, 0, len(targets))
	for _, target := range targets {
		logins = append(logins, target.Login)
	}
	f.calls = append(f.calls, logins)
}

type fakeMinerSyncer struct {
	calls [][]string
}

func (f *fakeMinerSyncer) Sync(_ context.Context, _ *auth.State, _ *twitch.Viewer, entries []twitch.StreamerEntry) {
	logins := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.ChannelID == "" {
			continue
		}
		logins = append(logins, entry.ChannelID)
	}
	f.calls = append(f.calls, logins)
}

func TestResolveStreamerEntries(t *testing.T) {
	service := streamService(
		channel("alpha", "1", "alpha_live"),
		resolveErr("beta", errors.New("lookup failed")),
		live("1", true),
	)

	updates := collectUpdates(t, service, []string{"alpha", "beta"}, nil, nil, cancelAfter(0))
	if len(updates) != 3 {
		t.Fatalf("len(updates) = %d", len(updates))
	}
	if updates[0].Viewer == nil || updates[0].Viewer.Login != "viewer" {
		t.Fatalf("viewer update = %#v", updates[0])
	}
	if entry := updates[1].Entry; entry == nil || entry.Status != twitch.StreamerReady || entry.Login != "alpha_live" || !entry.Live {
		t.Fatalf("alpha update = %#v", updates[1])
	}
	if entry := updates[2].Entry; entry == nil || entry.Status != twitch.StreamerError {
		t.Fatalf("beta update = %#v", updates[2])
	}
}

func TestResolveStreamerEntriesPlaybackFailureStillMarksLive(t *testing.T) {
	updates := collectUpdates(t, streamService(
		channel("alpha", "1", "alpha_live"),
		live("1", true),
		playbackErr("alpha_live", errors.New("token lookup failed")),
	), []string{"alpha"}, nil, nil, cancelAfter(0))

	if entry := updates[1].Entry; entry == nil || entry.Status != twitch.StreamerReady || !entry.Live {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestResolveStreamerEntriesRefreshesLiveState(t *testing.T) {
	updates := collectUpdates(t, streamService(
		channel("alpha", "1", "alpha_live"),
		streams("1", false, true),
	), []string{"alpha"}, nil, nil, cancelAfter(1))

	if len(updates) != 3 {
		t.Fatalf("len(updates) = %d", len(updates))
	}
	if updates[1].Entry == nil || updates[1].Entry.Live {
		t.Fatalf("first pass = %#v", updates[1])
	}
	if updates[2].Entry == nil || !updates[2].Entry.Live {
		t.Fatalf("second pass = %#v", updates[2])
	}
}

func TestResolveStreamerEntriesSyncsTopTwoLiveChannels(t *testing.T) {
	syncer := &fakeIRCSyncer{}
	miner := &fakeMinerSyncer{}
	collectUpdates(t, streamService(
		channel("alpha", "1", "alpha_live"),
		channel("beta", "2", "beta_live"),
		channel("gamma", "3", "gamma_live"),
		live("1", true),
		live("2", true),
		live("3", true),
	), []string{"alpha", "beta", "gamma"}, syncer, miner, cancelAfter(0))

	assertSyncCalls(t, syncer.calls, [][]string{
		{},
		{"alpha_live"},
		{"alpha_live", "beta_live"},
		{"alpha_live", "beta_live"},
	})
	assertSyncCalls(t, miner.calls, [][]string{
		{},
		{"1"},
		{"1", "2"},
		{"1", "2", "3"},
	})
}

func TestResolveStreamerEntriesRefreshUpdatesWatchedChannels(t *testing.T) {
	syncer := &fakeIRCSyncer{}
	miner := &fakeMinerSyncer{}
	collectUpdates(t, streamService(
		channel("alpha", "1", "alpha_live"),
		channel("beta", "2", "beta_live"),
		streams("1", true, false),
		streams("2", false, true),
	), []string{"alpha", "beta"}, syncer, miner, cancelAfter(1))

	assertSyncCalls(t, syncer.calls, [][]string{
		{},
		{"alpha_live"},
		{"alpha_live"},
		{},
		{"beta_live"},
	})
}

func TestNewMinerLogSinkBridgesMinerEntriesIntoStreamerUpdates(t *testing.T) {
	ch := make(chan StreamerUpdate, 1)

	newMinerLogSink(ch)(miner.LogEntry{
		Login: "alpha_live",
		Line:  "pubsub points earned: balance=42",
	})

	update := <-ch
	if update.Miner == nil {
		t.Fatal("expected miner update")
	}
	if update.Miner.Login != "alpha_live" || update.Miner.Line != "pubsub points earned: balance=42" {
		t.Fatalf("miner update = %#v", update.Miner)
	}
}

type serviceOption func(*fakeStreamerService)

func streamService(options ...serviceOption) *fakeStreamerService {
	service := &fakeStreamerService{
		channels:    map[string]*twitch.Channel{},
		resolveErrs: map[string]error{},
		streams:     map[string][]streamResult{},
		streamCalls: map[string]int{},
		playbackErr: map[string]error{},
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func channel(login, id, resolved string) serviceOption {
	return func(service *fakeStreamerService) {
		service.channels[login] = &twitch.Channel{ID: id, Login: resolved}
	}
}

func resolveErr(login string, err error) serviceOption {
	return func(service *fakeStreamerService) {
		service.resolveErrs[login] = err
	}
}

func live(channelID string, online bool) serviceOption {
	return streams(channelID, online)
}

func streams(channelID string, online ...bool) serviceOption {
	return func(service *fakeStreamerService) {
		for _, value := range online {
			service.streams[channelID] = append(service.streams[channelID], streamResult{info: &twitch.StreamInfo{Online: value}})
		}
	}
}

func playbackErr(login string, err error) serviceOption {
	return func(service *fakeStreamerService) {
		service.playbackErr[login] = err
	}
}

func collectUpdates(t *testing.T, service *fakeStreamerService, logins []string, ircSyncer ircSyncer, minerSyncer minerSyncer, sleep func(context.Context, time.Duration) error) []StreamerUpdate {
	t.Helper()
	var updates []StreamerUpdate
	err := resolveStreamerEntriesWithSleep(context.Background(), service, &auth.State{Login: "viewer", AccessToken: "token"}, logins, ircSyncer, minerSyncer, func(update StreamerUpdate) {
		updates = append(updates, update)
	}, 0, sleep)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	return updates
}

func cancelAfter(allowedSleeps int) func(context.Context, time.Duration) error {
	calls := 0
	return func(context.Context, time.Duration) error {
		calls++
		if calls <= allowedSleeps {
			return nil
		}
		return context.Canceled
	}
}

func assertSyncCalls(t *testing.T, got, want [][]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sync calls = %#v, want %#v", got, want)
	}
}
