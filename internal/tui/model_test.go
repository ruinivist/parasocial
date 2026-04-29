package tui

import (
	"fmt"
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
		"Info",
		"IRC",
		"Miner",
		"live | irc idle",
		"gamma",
		"loading",
		"Status: live",
		"IRC: not joined",
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

func TestFocusNavigationMovesBetweenPanels(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Live:        true,
		Status:      twitch.StreamerReady,
	})

	if model.focus != focusStreamers {
		t.Fatalf("initial focus = %v, want %v", model.focus, focusStreamers)
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRight})
	next := updated.(Model)
	if next.focus != focusInfo || next.visibleDetailTab() != infoTab {
		t.Fatalf("focus after first right = %v with tab %v, want %v and %v", next.focus, next.visibleDetailTab(), focusInfo, infoTab)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyRight})
	next = updated.(Model)
	if next.focus != focusIRC || next.visibleDetailTab() != ircTab {
		t.Fatalf("focus after second right = %v with tab %v, want %v and %v", next.focus, next.visibleDetailTab(), focusIRC, ircTab)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyRight})
	next = updated.(Model)
	if next.focus != focusMiner || next.visibleDetailTab() != minerTab {
		t.Fatalf("focus after third right = %v with tab %v, want %v and %v", next.focus, next.visibleDetailTab(), focusMiner, minerTab)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyRight})
	next = updated.(Model)
	if next.focus != focusMiner {
		t.Fatalf("focus after right on miner = %v, want %v", next.focus, focusMiner)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyLeft})
	next = updated.(Model)
	if next.focus != focusIRC {
		t.Fatalf("focus after left from miner = %v, want %v", next.focus, focusIRC)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyLeft})
	next = updated.(Model)
	if next.focus != focusInfo {
		t.Fatalf("focus after left from irc = %v, want %v", next.focus, focusInfo)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyLeft})
	next = updated.(Model)
	if next.focus != focusStreamers {
		t.Fatalf("focus after left from info = %v, want %v", next.focus, focusStreamers)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyLeft})
	next = updated.(Model)
	if next.focus != focusStreamers {
		t.Fatalf("focus after left on streamers = %v, want %v", next.focus, focusStreamers)
	}
}

func TestInfoFocusMakesUpDownNoop(t *testing.T) {
	model := dashboardModel(
		twitch.StreamerEntry{ConfigLogin: "alpha", Login: "alpha_live", Live: true, Status: twitch.StreamerReady},
		twitch.StreamerEntry{ConfigLogin: "beta", Login: "beta_live", Status: twitch.StreamerReady},
	)
	model.focus = focusInfo
	model.ircViewport.SetYOffset(0)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	next := updated.(Model)
	if next.selectedConfig != "alpha" {
		t.Fatalf("selectedConfig after down in info = %q, want alpha", next.selectedConfig)
	}
	if next.ircViewport.YOffset != 0 {
		t.Fatalf("irc viewport offset after down in info = %d, want 0", next.ircViewport.YOffset)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyUp})
	next = updated.(Model)
	if next.selectedConfig != "alpha" {
		t.Fatalf("selectedConfig after up in info = %q, want alpha", next.selectedConfig)
	}
}

func TestIRCUpdatesShowJoinedStatusAndFormattedMessages(t *testing.T) {
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

	updated, _ = next.Update(StreamerUpdate{IRC: &IRCUpdate{
		Login: "alpha_live",
		Line:  ":someone!someone@someone.tmi.twitch.tv PRIVMSG #alpha_live :hello there",
	}})
	next = updated.(Model)
	next.focus = focusIRC
	next.syncIRCViewport(true)

	assertContainsAll(t, next.View(), "someone: hello there")
}

func TestChatFocusUsesUpDownForViewportScroll(t *testing.T) {
	model := dashboardModel(
		twitch.StreamerEntry{ConfigLogin: "alpha", Login: "alpha_live", Live: true, Status: twitch.StreamerReady},
		twitch.StreamerEntry{ConfigLogin: "beta", Login: "beta_live", Status: twitch.StreamerReady},
	)
	model.focus = focusIRC
	model.ircDetails["alpha_live"] = ircDetail{
		joined:   true,
		messages: numberedMessages(20),
	}
	model.syncIRCViewport(true)
	model.ircViewport.GotoTop()

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	next := updated.(Model)
	if next.selectedConfig != "alpha" {
		t.Fatalf("selectedConfig after down in chat = %q, want alpha", next.selectedConfig)
	}
	if next.ircViewport.YOffset == 0 {
		t.Fatal("expected chat viewport to scroll down")
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyUp})
	next = updated.(Model)
	if next.ircViewport.YOffset != 0 {
		t.Fatalf("irc viewport offset after up in chat = %d, want 0", next.ircViewport.YOffset)
	}
}

func TestIRCUpdatesKeepOnlyLast50ChatMessages(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Live:        true,
		Status:      twitch.StreamerReady,
	})

	updated, _ := model.Update(StreamerUpdate{IRC: &IRCUpdate{Login: "alpha_live", State: IRCJoined}})
	next := updated.(Model)
	for i := 1; i <= maxIRCMessageHistory+5; i++ {
		line := fmt.Sprintf(":user%d!user%d@user%d.tmi.twitch.tv PRIVMSG #alpha_live :message %d", i, i, i, i)
		updated, _ = next.Update(StreamerUpdate{IRC: &IRCUpdate{Login: "alpha_live", Line: line}})
		next = updated.(Model)
	}

	detail := next.ircDetails["alpha_live"]
	if len(detail.messages) != maxIRCMessageHistory {
		t.Fatalf("message count = %d, want %d", len(detail.messages), maxIRCMessageHistory)
	}
	if detail.messages[0] != "user6: message 6" {
		t.Fatalf("oldest retained message = %q, want %q", detail.messages[0], "user6: message 6")
	}
	if detail.messages[len(detail.messages)-1] != "user55: message 55" {
		t.Fatalf("newest retained message = %q, want %q", detail.messages[len(detail.messages)-1], "user55: message 55")
	}
}

func TestMinerUpdatesRenderAndKeepOnlyLast50Messages(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Live:        true,
		Status:      twitch.StreamerReady,
	})

	next := model
	for i := 1; i <= maxIRCMessageHistory+5; i++ {
		updated, _ := next.Update(StreamerUpdate{Miner: &MinerUpdate{
			Login: "alpha_live",
			Line:  fmt.Sprintf("miner message %d", i),
		}})
		next = updated.(Model)
	}
	next.focus = focusMiner
	next.syncMinerViewport(true)

	logs := next.minerDetails["alpha_live"]
	if len(logs) != maxIRCMessageHistory {
		t.Fatalf("miner message count = %d, want %d", len(logs), maxIRCMessageHistory)
	}
	if logs[0] != "miner message 6" {
		t.Fatalf("oldest retained miner message = %q", logs[0])
	}
	if logs[len(logs)-1] != "miner message 55" {
		t.Fatalf("newest retained miner message = %q", logs[len(logs)-1])
	}
	assertContainsAll(t, next.View(), "miner message 55")
}

func TestMinerTabShowsHistoryForOfflineStreamer(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Status:      twitch.StreamerReady,
	})

	updated, _ := model.Update(StreamerUpdate{Miner: &MinerUpdate{
		Login: "alpha_live",
		Line:  "pubsub stream down",
	}})
	next := updated.(Model)
	next.focus = focusMiner
	next.syncMinerViewport(true)

	assertContainsAll(t, next.View(), "pubsub stream down")
}

func TestIRCUpdatesIgnoreNonChatProtocolLines(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Live:        true,
		Status:      twitch.StreamerReady,
	})

	updated, _ := model.Update(StreamerUpdate{IRC: &IRCUpdate{
		Login: "alpha_live",
		Line:  "Joined #alpha_live as viewer",
	}})
	next := updated.(Model)
	if len(next.ircDetails["alpha_live"].messages) != 0 {
		t.Fatalf("messages = %#v, want empty", next.ircDetails["alpha_live"].messages)
	}
}

func TestIRCViewportAutoScrollsAtBottom(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Live:        true,
		Status:      twitch.StreamerReady,
	})
	model.focus = focusIRC
	model.ircDetails["alpha_live"] = ircDetail{
		joined:   true,
		messages: numberedMessages(20),
	}
	model.syncIRCViewport(true)

	if !model.ircViewport.AtBottom() {
		t.Fatal("expected viewport to start at bottom")
	}

	updated, _ := model.Update(StreamerUpdate{IRC: &IRCUpdate{
		Login: "alpha_live",
		Line:  ":late!late@late.tmi.twitch.tv PRIVMSG #alpha_live :newest",
	}})
	next := updated.(Model)
	if !next.ircViewport.AtBottom() {
		t.Fatal("expected viewport to stay at bottom after new message")
	}
}

func TestIRCViewportPreservesManualScrollPosition(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Live:        true,
		Status:      twitch.StreamerReady,
	})
	model.focus = focusIRC
	model.ircDetails["alpha_live"] = ircDetail{
		joined:   true,
		messages: numberedMessages(20),
	}
	model.syncIRCViewport(true)
	model.ircViewport.GotoTop()

	updated, _ := model.Update(StreamerUpdate{IRC: &IRCUpdate{
		Login: "alpha_live",
		Line:  ":late!late@late.tmi.twitch.tv PRIVMSG #alpha_live :newest",
	}})
	next := updated.(Model)
	if next.ircViewport.YOffset != 0 {
		t.Fatalf("viewport YOffset = %d, want 0", next.ircViewport.YOffset)
	}
	if next.ircViewport.AtBottom() {
		t.Fatal("expected viewport to remain off bottom after manual scroll")
	}
}

func TestMinerViewportAutoScrollsAtBottom(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Live:        true,
		Status:      twitch.StreamerReady,
	})
	model.focus = focusMiner
	model.minerDetails["alpha_live"] = numberedMinerMessages(20)
	model.syncMinerViewport(true)

	if !model.minerViewport.AtBottom() {
		t.Fatal("expected miner viewport to start at bottom")
	}

	updated, _ := model.Update(StreamerUpdate{Miner: &MinerUpdate{
		Login: "alpha_live",
		Line:  "newest miner event",
	}})
	next := updated.(Model)
	if !next.minerViewport.AtBottom() {
		t.Fatal("expected miner viewport to stay at bottom after new message")
	}
}

func TestMinerViewportPreservesManualScrollPosition(t *testing.T) {
	model := dashboardModel(twitch.StreamerEntry{
		ConfigLogin: "alpha",
		Login:       "alpha_live",
		Live:        true,
		Status:      twitch.StreamerReady,
	})
	model.focus = focusMiner
	model.minerDetails["alpha_live"] = numberedMinerMessages(20)
	model.syncMinerViewport(true)
	model.minerViewport.GotoTop()

	updated, _ := model.Update(StreamerUpdate{Miner: &MinerUpdate{
		Login: "alpha_live",
		Line:  "newest miner event",
	}})
	next := updated.(Model)
	if next.minerViewport.YOffset != 0 {
		t.Fatalf("miner viewport YOffset = %d, want 0", next.minerViewport.YOffset)
	}
	if next.minerViewport.AtBottom() {
		t.Fatal("expected miner viewport to remain off bottom after manual scroll")
	}
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
	model.resizeComponents()
	model.syncDetailViewports(true)
	return model
}

func numberedMessages(count int) []string {
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		lines = append(lines, fmt.Sprintf("user%d: message %d", i, i))
	}
	return lines
}

func numberedMinerMessages(count int) []string {
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		lines = append(lines, fmt.Sprintf("miner message %d", i))
	}
	return lines
}

func assertContainsAll(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}
