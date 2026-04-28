package tui

import (
	"testing"

	"parasocial/internal/auth"
)

func TestViewDisplaysStreamers(t *testing.T) {
	model := New(Options{
		Streamers: []string{"alpha", "beta"},
	})
	model.mode = streamerView

	got := model.View()
	want := "Streamers\n1. alpha\n2. beta\n\n"
	if got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}

func TestAuthUpdateAppendsLogLine(t *testing.T) {
	model := New(Options{Streamers: []string{"alpha"}})

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

func TestAuthSuccessSwitchesToStreamerView(t *testing.T) {
	model := New(Options{Streamers: []string{"alpha"}})

	updated, _ := model.Update(AuthUpdate{
		State: &auth.State{Login: "viewer"},
		Done:  true,
	})

	next := updated.(Model)
	got := next.View()
	want := "Logged in as viewer\n\nStreamers\n1. alpha\n\n"
	if got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}
