package tui

import "testing"

func TestViewDisplaysStreamers(t *testing.T) {
	model := New([]string{"alpha", "beta"})

	got := model.View()
	want := "Streamers\n1. alpha\n2. beta\n\n"
	if got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}
