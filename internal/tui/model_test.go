package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"parasocial/internal/auth"
	"parasocial/internal/twitch"
)

func TestViewDisplaysSplitPaneWithSelectedStreamerDetails(t *testing.T) {
	model := New(Options{
		Streamers: []twitch.StreamerEntry{
			{ConfigLogin: "alpha", Login: "alpha_live", ChannelID: "1", Live: true, Status: twitch.StreamerReady},
			{ConfigLogin: "beta", Login: "beta_live", ChannelID: "2", Live: true, Status: twitch.StreamerReady},
			{ConfigLogin: "gamma", Status: twitch.StreamerLoading},
		},
	})
	model.mode = streamerView
	model.viewer = &twitch.Viewer{ID: "7", Login: "viewer"}
	model.width = 80

	got := model.View()
	if !strings.Contains(got, "Watching: alpha_live, beta_live\n\n") {
		t.Fatalf("View() missing watching summary:\n%s", got)
	}
	if !strings.Contains(got, "> alpha_live [active]") {
		t.Fatalf("View() missing selected active row:\n%s", got)
	}
	if !strings.Contains(got, "gamma [inactive]") {
		t.Fatalf("View() missing inactive row:\n%s", got)
	}
	if !strings.Contains(got, "IRC Chat") || !strings.Contains(got, "not joined") {
		t.Fatalf("View() missing detail pane:\n%s", got)
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
	if !strings.Contains(got, "Watching: no live streamers") {
		t.Fatalf("View() = %q", got)
	}
	if next.selectedConfig != "alpha" {
		t.Fatalf("selectedConfig = %q, want alpha", next.selectedConfig)
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
			Live:        true,
			Status:      twitch.StreamerReady,
		},
	})
	if cmd != nil {
		t.Fatal("expected nil cmd when no update channel is attached")
	}

	next := updated.(Model)
	if next.selectedConfig != "alpha" {
		t.Fatalf("selectedConfig = %q, want alpha", next.selectedConfig)
	}
}

func TestViewDisplaysInactiveDetailForOfflineStreamer(t *testing.T) {
	model := New(Options{
		Streamers: []twitch.StreamerEntry{
			{ConfigLogin: "alpha", Login: "alpha_live", ChannelID: "1", Status: twitch.StreamerReady},
		},
	})
	model.mode = streamerView
	model.viewer = &twitch.Viewer{ID: "7", Login: "viewer"}
	model.width = 80

	got := model.View()
	if !strings.Contains(got, "inactive") {
		t.Fatalf("View() = %q", got)
	}
}

func TestActiveStreamersRenderBeforeInactiveInConfigOrder(t *testing.T) {
	model := New(Options{
		Streamers: []twitch.StreamerEntry{
			{ConfigLogin: "alpha", Login: "alpha_live", Status: twitch.StreamerReady},
			{ConfigLogin: "beta", Login: "beta_live", Live: true, Status: twitch.StreamerReady},
			{ConfigLogin: "gamma", Login: "gamma_live", Live: true, Status: twitch.StreamerReady},
			{ConfigLogin: "delta", Status: twitch.StreamerLoading},
		},
	})

	rows := model.orderedRows()
	got := []string{rows[0].entry.ConfigLogin, rows[1].entry.ConfigLogin, rows[2].entry.ConfigLogin, rows[3].entry.ConfigLogin}
	want := []string{"beta", "gamma", "alpha", "delta"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("row order = %#v, want %#v", got, want)
		}
	}
}

func TestSelectionTracksSameStreamerAcrossReorder(t *testing.T) {
	model := New(Options{
		Streamers: []twitch.StreamerEntry{
			{ConfigLogin: "alpha", Login: "alpha_live", Status: twitch.StreamerReady},
			{ConfigLogin: "beta", Login: "beta_live", Status: twitch.StreamerReady},
		},
		AuthState: &auth.State{Login: "viewer"},
	})
	model.mode = streamerView
	model.selectedConfig = "beta"

	updated, _ := model.Update(StreamerUpdate{
		Index: 1,
		Entry: &twitch.StreamerEntry{
			ConfigLogin: "beta",
			Login:       "beta_live",
			Live:        true,
			Status:      twitch.StreamerReady,
		},
	})
	next := updated.(Model)

	if next.selectedConfig != "beta" {
		t.Fatalf("selectedConfig = %q, want beta", next.selectedConfig)
	}
	if next.selectedRowIndex(next.orderedRows()) != 0 {
		t.Fatalf("selected row index = %d, want 0", next.selectedRowIndex(next.orderedRows()))
	}
}

func TestUpDownNavigationMovesSelectedStreamer(t *testing.T) {
	model := New(Options{
		Streamers: []twitch.StreamerEntry{
			{ConfigLogin: "alpha", Login: "alpha_live", Live: true, Status: twitch.StreamerReady},
			{ConfigLogin: "beta", Login: "beta_live", Status: twitch.StreamerReady},
		},
		AuthState: &auth.State{Login: "viewer"},
	})
	model.mode = streamerView
	model.selectedConfig = "alpha"

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
	model := New(Options{
		Streamers: []twitch.StreamerEntry{
			{ConfigLogin: "alpha", Login: "alpha_live", Live: true, Status: twitch.StreamerReady},
		},
		AuthState: &auth.State{Login: "viewer"},
	})
	model.mode = streamerView
	model.width = 80

	updated, _ := model.Update(StreamerUpdate{
		IRC: &IRCUpdate{
			Login: "alpha_live",
			State: IRCJoined,
		},
	})
	next := updated.(Model)

	detail := next.ircDetails["alpha_live"]
	if !detail.joined {
		t.Fatal("expected joined detail")
	}
	if !strings.Contains(next.View(), greenDot) {
		t.Fatalf("View() missing green status dot:\n%s", next.View())
	}
}

func TestWindowSizeClipsListAroundSelection(t *testing.T) {
	model := New(Options{
		Streamers: []twitch.StreamerEntry{
			{ConfigLogin: "alpha", Status: twitch.StreamerReady},
			{ConfigLogin: "beta", Status: twitch.StreamerReady},
			{ConfigLogin: "gamma", Status: twitch.StreamerReady},
			{ConfigLogin: "delta", Status: twitch.StreamerReady},
		},
		AuthState: &auth.State{Login: "viewer"},
	})
	model.mode = streamerView
	model.selectedConfig = "delta"

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 60, Height: 6})
	next := updated.(Model)
	got := next.View()
	if !strings.Contains(got, "> delta [inactive]") {
		t.Fatalf("View() missing selected row after clipping:\n%s", got)
	}
}
