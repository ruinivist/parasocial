package tui

import (
	"testing"

	"parasocial/internal/auth"
	"parasocial/internal/twitch"
)

func TestViewDisplaysStreamers(t *testing.T) {
	model := New(Options{
		Streamers: []twitch.StreamerEntry{
			{ConfigLogin: "alpha", Login: "alpha_live", ChannelID: "1", Status: twitch.StreamerReady},
			{ConfigLogin: "beta", Status: twitch.StreamerLoading},
		},
	})
	model.mode = streamerView
	model.viewer = &twitch.Viewer{ID: "7", Login: "viewer"}

	got := model.View()
	want := "Logged in as viewer (7)\n\nStreamers\n1. alpha_live (1)\n2. beta [loading]\n\n"
	if got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}

func TestAuthUpdateAppendsLogLine(t *testing.T) {
	model := New(Options{Streamers: twitch.LoadingStreamerEntries([]string{"alpha"})})

	updated, cmd := model.Update(AuthUpdate{Line: "Open page: https://www.twitch.tv/activate"})
	if cmd != nil {
		t.Fatal("expected nil cmd after auth update without channel")
	}

	next := updated.(Model)
	got := next.View()
	want := "Twitch Login\nOpen page: https://www.twitch.tv/activate\n\n"
	if got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}

func TestAuthSuccessSwitchesToStreamerViewAndStartsResolution(t *testing.T) {
	var started *auth.State
	model := New(Options{
		Streamers: twitch.LoadingStreamerEntries([]string{"alpha"}),
		StartResolve: func(state *auth.State, ch chan<- StreamerUpdate) {
			started = state
			close(ch)
		},
	})

	state := &auth.State{Login: "viewer", UserID: "7"}

	updated, cmd := model.Update(AuthUpdate{
		State: state,
		Done:  true,
	})
	if cmd == nil {
		t.Fatal("expected streamer resolution command")
	}
	if _, ok := cmd().(streamerStartedMsg); !ok {
		t.Fatalf("cmd() returned %T, want streamerStartedMsg", cmd())
	}
	if started != state {
		t.Fatalf("started state = %#v, want %#v", started, state)
	}

	next := updated.(Model)
	got := next.View()
	want := "Logged in as viewer\nResolving viewer identity...\n\nStreamers\n1. alpha [loading]\n\n"
	if got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}

func TestInitStartsResolutionWhenAlreadyAuthenticated(t *testing.T) {
	var started *auth.State
	state := &auth.State{Login: "viewer", UserID: "7"}
	model := New(Options{
		Streamers: twitch.LoadingStreamerEntries([]string{"alpha"}),
		AuthState: state,
		StartResolve: func(got *auth.State, ch chan<- StreamerUpdate) {
			started = got
			close(ch)
		},
	})

	cmd := model.Init()
	if cmd == nil {
		t.Fatal("expected init command")
	}
	if _, ok := cmd().(streamerStartedMsg); !ok {
		t.Fatalf("cmd() returned %T, want streamerStartedMsg", cmd())
	}
	if started != state {
		t.Fatalf("started state = %#v, want %#v", started, state)
	}
}

func TestStreamerUpdateAppliesEntry(t *testing.T) {
	model := New(Options{
		Streamers: twitch.LoadingStreamerEntries([]string{"alpha"}),
		AuthState: &auth.State{Login: "viewer"},
	})
	model.mode = streamerView

	updated, cmd := model.Update(StreamerUpdate{
		Viewer: &twitch.Viewer{ID: "7", Login: "viewer"},
		Index:  0,
		Entry: &twitch.StreamerEntry{
			ConfigLogin: "alpha",
			Login:       "alpha_live",
			ChannelID:   "1",
			Status:      twitch.StreamerReady,
		},
	})
	if cmd != nil {
		t.Fatal("expected nil cmd when no update channel is attached")
	}

	next := updated.(Model)
	got := next.View()
	want := "Logged in as viewer (7)\n\nStreamers\n1. alpha_live (1)\n\n"
	if got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}
