// app_test.go covers the app-layer orchestration around Twitch viewer and streamer resolution.
// It exercises the background resolution loop independently of Bubble Tea startup
// so the app wiring can be validated without requiring a full interactive session.
package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"parasocial/internal/auth"
	"parasocial/internal/irc"
	"parasocial/internal/tui"
	"parasocial/internal/twitch"
)

// fakeStreamerService is a test double for the app's Twitch resolution dependency.
type fakeStreamerService struct {
	viewer      *twitch.Viewer
	viewerErr   error
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

// CurrentUser returns the configured fake viewer or viewer error.
func (f *fakeStreamerService) CurrentUser(context.Context) (*twitch.Viewer, error) {
	if f.viewerErr != nil {
		return nil, f.viewerErr
	}
	return f.viewer, nil
}

// ResolveStreamer returns the configured fake channel or resolution error.
func (f *fakeStreamerService) ResolveStreamer(_ context.Context, login string) (*twitch.Channel, error) {
	if err, ok := f.resolveErrs[login]; ok {
		return nil, err
	}
	return f.channels[login], nil
}

func (f *fakeStreamerService) StreamInfo(_ context.Context, channelID string) (*twitch.StreamInfo, error) {
	if f.streamCalls == nil {
		f.streamCalls = map[string]int{}
	}
	call := f.streamCalls[channelID]
	f.streamCalls[channelID] = call + 1

	results := f.streams[channelID]
	if len(results) == 0 {
		return &twitch.StreamInfo{Online: false}, nil
	}
	if call >= len(results) {
		call = len(results) - 1
	}
	result := results[call]
	if result.err != nil {
		return nil, result.err
	}
	return result.info, nil
}

func (f *fakeStreamerService) PlaybackAccessToken(_ context.Context, login string) (*twitch.PlaybackToken, error) {
	if err, ok := f.playbackErr[login]; ok {
		return nil, err
	}
	return &twitch.PlaybackToken{Signature: "sig", Value: "token"}, nil
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

// TestResolveStreamerEntries verifies that app resolution emits viewer, success, and error updates.
func TestResolveStreamerEntries(t *testing.T) {
	t.Parallel()

	service := &fakeStreamerService{
		viewer: &twitch.Viewer{ID: "7", Login: "viewer"},
		channels: map[string]*twitch.Channel{
			"alpha": {ID: "1", Login: "alpha_live"},
		},
		resolveErrs: map[string]error{
			"beta": errors.New("lookup failed"),
		},
		streams: map[string][]streamResult{
			"1": {{info: &twitch.StreamInfo{Online: true}}},
		},
	}

	var updates []tui.StreamerUpdate
	err := resolveStreamerEntriesWithSleep(context.Background(), service, &auth.State{Login: "viewer", AccessToken: "token"}, []string{"alpha", "beta"}, nil, func(update tui.StreamerUpdate) {
		updates = append(updates, update)
	}, 0, func(context.Context, time.Duration) error {
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(updates) != 3 {
		t.Fatalf("len(updates) = %d", len(updates))
	}
	if updates[0].Viewer == nil || updates[0].Viewer.Login != "viewer" {
		t.Fatalf("viewer update = %#v", updates[0])
	}
	if updates[1].Entry == nil || updates[1].Entry.Status != twitch.StreamerReady || updates[1].Entry.Login != "alpha_live" || !updates[1].Entry.Live {
		t.Fatalf("alpha update = %#v", updates[1])
	}
	if updates[2].Entry == nil || updates[2].Entry.Status != twitch.StreamerError {
		t.Fatalf("beta update = %#v", updates[2])
	}
}

func TestResolveStreamerEntriesPlaybackFailureStillMarksLive(t *testing.T) {
	t.Parallel()

	service := &fakeStreamerService{
		viewer: &twitch.Viewer{ID: "7", Login: "viewer"},
		channels: map[string]*twitch.Channel{
			"alpha": {ID: "1", Login: "alpha_live"},
		},
		streams: map[string][]streamResult{
			"1": {{info: &twitch.StreamInfo{Online: true}}},
		},
		playbackErr: map[string]error{
			"alpha_live": errors.New("token lookup failed"),
		},
	}

	var updates []tui.StreamerUpdate
	err := resolveStreamerEntriesWithSleep(context.Background(), service, &auth.State{Login: "viewer", AccessToken: "token"}, []string{"alpha"}, nil, func(update tui.StreamerUpdate) {
		updates = append(updates, update)
	}, 0, func(context.Context, time.Duration) error {
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(updates) != 2 {
		t.Fatalf("len(updates) = %d", len(updates))
	}
	entry := updates[1].Entry
	if entry == nil || entry.Status != twitch.StreamerReady || !entry.Live {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestResolveStreamerEntriesRefreshesLiveState(t *testing.T) {
	t.Parallel()

	service := &fakeStreamerService{
		viewer: &twitch.Viewer{ID: "7", Login: "viewer"},
		channels: map[string]*twitch.Channel{
			"alpha": {ID: "1", Login: "alpha_live"},
		},
		streams: map[string][]streamResult{
			"1": {
				{info: &twitch.StreamInfo{Online: false}},
				{info: &twitch.StreamInfo{Online: true}},
			},
		},
	}

	var updates []tui.StreamerUpdate
	sleepCalls := 0
	err := resolveStreamerEntriesWithSleep(context.Background(), service, &auth.State{Login: "viewer", AccessToken: "token"}, []string{"alpha"}, nil, func(update tui.StreamerUpdate) {
		updates = append(updates, update)
	}, 0, func(context.Context, time.Duration) error {
		sleepCalls++
		if sleepCalls == 1 {
			return nil
		}
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
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
	t.Parallel()

	service := &fakeStreamerService{
		viewer: &twitch.Viewer{ID: "7", Login: "viewer"},
		channels: map[string]*twitch.Channel{
			"alpha": {ID: "1", Login: "alpha_live"},
			"beta":  {ID: "2", Login: "beta_live"},
			"gamma": {ID: "3", Login: "gamma_live"},
		},
		streams: map[string][]streamResult{
			"1": {{info: &twitch.StreamInfo{Online: true}}},
			"2": {{info: &twitch.StreamInfo{Online: true}}},
			"3": {{info: &twitch.StreamInfo{Online: true}}},
		},
	}
	syncer := &fakeIRCSyncer{}

	err := resolveStreamerEntriesWithSleep(context.Background(), service, &auth.State{Login: "viewer", AccessToken: "token"}, []string{"alpha", "beta", "gamma"}, syncer, func(tui.StreamerUpdate) {
	}, 0, func(context.Context, time.Duration) error {
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	want := [][]string{
		{},
		{"alpha_live"},
		{"alpha_live", "beta_live"},
		{"alpha_live", "beta_live"},
	}
	if !reflect.DeepEqual(syncer.calls, want) {
		t.Fatalf("sync calls = %#v, want %#v", syncer.calls, want)
	}
}

func TestResolveStreamerEntriesRefreshUpdatesWatchedChannels(t *testing.T) {
	t.Parallel()

	service := &fakeStreamerService{
		viewer: &twitch.Viewer{ID: "7", Login: "viewer"},
		channels: map[string]*twitch.Channel{
			"alpha": {ID: "1", Login: "alpha_live"},
			"beta":  {ID: "2", Login: "beta_live"},
		},
		streams: map[string][]streamResult{
			"1": {
				{info: &twitch.StreamInfo{Online: true}},
				{info: &twitch.StreamInfo{Online: false}},
			},
			"2": {
				{info: &twitch.StreamInfo{Online: false}},
				{info: &twitch.StreamInfo{Online: true}},
			},
		},
	}
	syncer := &fakeIRCSyncer{}

	sleepCalls := 0
	err := resolveStreamerEntriesWithSleep(context.Background(), service, &auth.State{Login: "viewer", AccessToken: "token"}, []string{"alpha", "beta"}, syncer, func(tui.StreamerUpdate) {
	}, 0, func(context.Context, time.Duration) error {
		sleepCalls++
		if sleepCalls == 1 {
			return nil
		}
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	want := [][]string{
		{},
		{"alpha_live"},
		{"alpha_live"},
		{},
		{"beta_live"},
	}
	if !reflect.DeepEqual(syncer.calls, want) {
		t.Fatalf("sync calls = %#v, want %#v", syncer.calls, want)
	}
}
