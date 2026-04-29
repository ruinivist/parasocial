package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"parasocial/internal/auth"
	"parasocial/internal/twitch"
)

type fakeModelRuntime struct {
	authStarted    bool
	resolveStarted *auth.State
}

func (f *fakeModelRuntime) startAuth(ch chan<- AuthUpdate) {
	f.authStarted = true
	close(ch)
}

func (f *fakeModelRuntime) startResolve(state *auth.State, ch chan<- StreamerUpdate) {
	f.resolveStarted = state
	close(ch)
}

func TestViewDisplaysDashboardWithSelectedStreamerDetails(t *testing.T) {
	model := dashboardModel(
		twitch.StreamerEntry{ConfigLogin: "alpha", Login: "alpha_live", Live: true, Status: twitch.StreamerReady},
		twitch.StreamerEntry{ConfigLogin: "beta", Login: "beta_live", Live: true, Status: twitch.StreamerReady},
		twitch.StreamerEntry{ConfigLogin: "gamma", Status: twitch.StreamerLoading},
	)

	assertContainsAll(t, model.View(),
		"Watching: alpha_live, beta_live",
		"alpha_live",
		"live | irc idle",
		"gamma",
		"loading",
		"IRC Chat",
		"not joined",
	)
}

func TestAuthUpdateAppendsLogLine(t *testing.T) {
	updated, cmd := New(Options{Streamers: []string{"alpha"}}).Update(AuthUpdate{Line: "Open page: https://www.twitch.tv/activate"})
	if cmd != nil {
		t.Fatal("expected nil cmd after auth update without channel")
	}
	assertContainsAll(t, updated.(Model).View(), "Twitch Login", "Open page: https://www.twitch.tv/activate")
}

func TestAuthSuccessSwitchesToStreamerViewAndStartsResolution(t *testing.T) {
	runtime := &fakeModelRuntime{}
	state := &auth.State{Login: "viewer", UserID: "7"}
	updated, cmd := New(Options{Streamers: []string{"alpha"}, runtime: runtime}).Update(AuthUpdate{State: state, Done: true})
	if cmd == nil {
		t.Fatal("expected streamer resolution command")
	}
	if _, ok := cmd().(streamerStartedMsg); !ok {
		t.Fatalf("cmd() returned %T, want streamerStartedMsg", cmd())
	}
	if runtime.resolveStarted != state {
		t.Fatalf("started state = %#v, want %#v", runtime.resolveStarted, state)
	}

	next := updated.(Model)
	assertContainsAll(t, next.View(), "Watching: no live streamers")
	if next.selectedConfig != "alpha" {
		t.Fatalf("selectedConfig = %q, want alpha", next.selectedConfig)
	}
}

func TestInitStartsAuthOrResolution(t *testing.T) {
	runtime := &fakeModelRuntime{}
	if _, ok := New(Options{Streamers: []string{"alpha"}, runtime: runtime}).Init()().(authStartedMsg); !ok {
		t.Fatal("unauthenticated Init() did not start auth")
	}
	if !runtime.authStarted {
		t.Fatal("expected auth runtime to start")
	}

	state := &auth.State{Login: "viewer", UserID: "7"}
	runtime = &fakeModelRuntime{}
	if _, ok := New(Options{Streamers: []string{"alpha"}, AuthState: state, runtime: runtime}).Init()().(streamerStartedMsg); !ok {
		t.Fatal("authenticated Init() did not start streamer resolution")
	}
	if runtime.resolveStarted != state {
		t.Fatalf("started state = %#v, want %#v", runtime.resolveStarted, state)
	}
}

func TestStreamerUpdateAppliesEntry(t *testing.T) {
	model := New(Options{Streamers: []string{"alpha"}, AuthState: &auth.State{Login: "viewer"}})
	model.mode = streamerView

	updated, cmd := model.Update(StreamerUpdate{
		Viewer: &twitch.Viewer{ID: "7", Login: "viewer"},
		Index:  0,
		Entry: &twitch.StreamerEntry{
			ConfigLogin: "alpha",
			Login:       "alpha_live",
			ChannelID:   "1",
			Live:        true,
			Status:      twitch.StreamerReady,
		},
	})
	if cmd != nil {
		t.Fatal("expected nil cmd when no update channel is attached")
	}

	next := updated.(Model)
	assertContainsAll(t, next.View(), "alpha_live", "live")
	if next.selectedConfig != "alpha" {
		t.Fatalf("selectedConfig = %q, want alpha", next.selectedConfig)
	}
}

func TestViewDisplaysInactiveDetailForOfflineStreamer(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{ConfigLogin: "alpha", Login: "alpha_live", Status: twitch.StreamerReady})
	assertContainsAll(t, model.View(), "offline", "inactive")
}

func TestActiveStreamersRenderBeforeInactiveInConfigOrder(t *testing.T) {
	model := New(Options{initialStreamers: []twitch.StreamerEntry{
		{ConfigLogin: "alpha", Login: "alpha_live", Status: twitch.StreamerReady},
		{ConfigLogin: "beta", Login: "beta_live", Live: true, Status: twitch.StreamerReady},
		{ConfigLogin: "gamma", Login: "gamma_live", Live: true, Status: twitch.StreamerReady},
		{ConfigLogin: "delta", Status: twitch.StreamerLoading},
	}})

	got := []string{}
	for _, entry := range model.orderedStreamers() {
		got = append(got, entry.ConfigLogin)
	}
	want := []string{"beta", "gamma", "alpha", "delta"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("row order = %#v, want %#v", got, want)
		}
	}
}

func TestSelectionTracksSameStreamerAcrossReorder(t *testing.T) {
	model := New(Options{
		initialStreamers: []twitch.StreamerEntry{
			{ConfigLogin: "alpha", Login: "alpha_live", Status: twitch.StreamerReady},
			{ConfigLogin: "beta", Login: "beta_live", Status: twitch.StreamerReady},
		},
		AuthState: &auth.State{Login: "viewer"},
	})
	model.mode = streamerView
	model.selectedConfig = "beta"

	updated, _ := model.Update(StreamerUpdate{
		Index: 1,
		Entry: &twitch.StreamerEntry{ConfigLogin: "beta", Login: "beta_live", Live: true, Status: twitch.StreamerReady},
	})
	next := updated.(Model)
	if next.selectedConfig != "beta" || next.selectedRowIndex(next.orderedStreamers()) != 0 {
		t.Fatalf("selection moved after reorder: %q at %d", next.selectedConfig, next.selectedRowIndex(next.orderedStreamers()))
	}
}

func TestUpDownNavigationMovesSelectedStreamer(t *testing.T) {
	model := dashboardModel(
		twitch.StreamerEntry{ConfigLogin: "alpha", Login: "alpha_live", Live: true, Status: twitch.StreamerReady},
		twitch.StreamerEntry{ConfigLogin: "beta", Login: "beta_live", Status: twitch.StreamerReady},
	)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	next := updated.(Model)
	if next.selectedConfig != "beta" {
		t.Fatalf("selectedConfig after down = %q, want beta", next.selectedConfig)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyUp})
	next = updated.(Model)
	if next.selectedConfig != "alpha" {
		t.Fatalf("selectedConfig after up = %q, want alpha", next.selectedConfig)
	}
}

func TestIRCUpdatesShowJoinedStatusOnly(t *testing.T) {
	updated, _ := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Live:        true,
		Status:      twitch.StreamerReady,
	}).Update(StreamerUpdate{IRC: &IRCUpdate{Login: "alpha_live", State: IRCJoined}})
	next := updated.(Model)

	if !next.ircDetails["alpha_live"].joined {
		t.Fatal("expected joined detail")
	}
	assertContainsAll(t, next.View(), "joined")
}

func TestWindowSizeKeepsSelectionVisible(t *testing.T) {
	model := dashboardModel(
		twitch.StreamerEntry{ConfigLogin: "alpha", Status: twitch.StreamerReady},
		twitch.StreamerEntry{ConfigLogin: "beta", Status: twitch.StreamerReady},
		twitch.StreamerEntry{ConfigLogin: "gamma", Status: twitch.StreamerReady},
		twitch.StreamerEntry{ConfigLogin: "delta", Status: twitch.StreamerReady},
	)
	model.selectedConfig = "delta"

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	assertContainsAll(t, updated.(Model).View(), "delta")
}

func dashboardModel(entries ...twitch.StreamerEntry) Model {
	model := New(Options{initialStreamers: entries, AuthState: &auth.State{Login: "viewer"}})
	model.mode = streamerView
	model.viewer = &twitch.Viewer{ID: "7", Login: "viewer"}
	model.width = 100
	model.height = 28
	return model
}

func assertContainsAll(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}
