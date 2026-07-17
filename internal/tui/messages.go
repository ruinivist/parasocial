package tui

import (
	"parasocial/internal/auth"
	"parasocial/internal/irc"
	"parasocial/internal/miner"
	"parasocial/internal/twitch"
)

// AuthUpdate carries one incremental auth log line or completion result into the TUI.
type AuthUpdate struct {
	Line  string
	State *auth.State
	Err   error
	Done  bool
}

// StreamerUpdate carries one streamer resolution update into the TUI.
type StreamerUpdate struct {
	Viewer      *twitch.Viewer
	Entry       *twitch.StreamerEntry
	IRC         *irc.Event
	MinerLog    *miner.LogEntry
	MinerStatus *miner.StatusEntry
	Index       int
	Err         error
	Done        bool
}

type authStartedMsg struct {
	Updates <-chan AuthUpdate
}

type streamerStartedMsg struct {
	Updates <-chan StreamerUpdate
}
