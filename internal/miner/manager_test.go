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
	playbackErr   map[string]error

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

func (f *fakeService) ClaimCommunityPoints(_ context.Context, channelID, claimID string) error {
	f.claimed = append(f.claimed, channelID+":"+claimID)
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
	manager := NewManager(context.Background(), service, pubsub)
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
	manager := NewManager(context.Background(), service, &fakePubSub{})
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

func TestHandlePubSubEventUpdatesBalanceAndClaims(t *testing.T) {
	service := &fakeService{}
	manager := NewManager(context.Background(), service, &fakePubSub{})
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

func cancelSleep(context.Context, time.Duration) error { return context.Canceled }

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
