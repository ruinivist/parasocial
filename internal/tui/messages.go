package tui

import (
	"parasocial/internal/auth"
	"parasocial/internal/twitch"
)

// IRCState describes the current IRC join lifecycle for one streamer row.
type IRCState string

const (
	IRCPending      IRCState = "pending"
	IRCJoined       IRCState = "joined"
	IRCDisconnected IRCState = "disconnected"
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
	Viewer *twitch.Viewer
	Entry  *twitch.StreamerEntry
	IRC    *IRCUpdate
	Miner  *MinerUpdate
	Index  int
	Err    error
	Done   bool
}

// IRCUpdate carries one IRC connection state or log line into the TUI.
type IRCUpdate struct {
	Login string
	State IRCState
	Line  string
}

// MinerUpdate carries one miner log line into the TUI.
type MinerUpdate struct {
	Login string
	Line  string
}

type authStartedMsg struct {
	Updates <-chan AuthUpdate
}

type streamerStartedMsg struct {
	Updates <-chan StreamerUpdate
}
