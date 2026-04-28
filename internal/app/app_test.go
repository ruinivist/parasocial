// app_test.go covers the app-layer orchestration around Twitch viewer and streamer resolution.
// It exercises the background resolution loop independently of Bubble Tea startup
// so the app wiring can be validated without requiring a full interactive session.
package app

import (
	"context"
	"errors"
	"testing"

	"parasocial/internal/tui"
	"parasocial/internal/twitch"
)

// fakeStreamerService is a test double for the app's Twitch resolution dependency.
type fakeStreamerService struct {
	viewer    *twitch.Viewer
	viewerErr error
	channels  map[string]*twitch.Channel
	errs      map[string]error
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
	if err, ok := f.errs[login]; ok {
		return nil, err
	}
	return f.channels[login], nil
}

// TestResolveStreamerEntries verifies that app resolution emits viewer, success, and error updates.
func TestResolveStreamerEntries(t *testing.T) {
	t.Parallel()

	service := &fakeStreamerService{
		viewer: &twitch.Viewer{ID: "7", Login: "viewer"},
		channels: map[string]*twitch.Channel{
			"alpha": {ID: "1", Login: "alpha_live"},
		},
		errs: map[string]error{
			"beta": errors.New("lookup failed"),
		},
	}

	var updates []tui.StreamerUpdate
	err := resolveStreamerEntries(context.Background(), service, []string{"alpha", "beta"}, func(update tui.StreamerUpdate) {
		updates = append(updates, update)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 3 {
		t.Fatalf("len(updates) = %d", len(updates))
	}
	if updates[0].Viewer == nil || updates[0].Viewer.Login != "viewer" {
		t.Fatalf("viewer update = %#v", updates[0])
	}
	if updates[1].Entry == nil || updates[1].Entry.Status != twitch.StreamerReady || updates[1].Entry.Login != "alpha_live" {
		t.Fatalf("alpha update = %#v", updates[1])
	}
	if updates[2].Entry == nil || updates[2].Entry.Status != twitch.StreamerError {
		t.Fatalf("beta update = %#v", updates[2])
	}
}
