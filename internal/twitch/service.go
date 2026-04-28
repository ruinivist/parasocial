// service.go defines the Twitch domain lookups the rewritten app currently needs.
// It wraps the lower-level GraphQL client with small typed methods for the viewer
// identity and configured streamer resolution used to populate the terminal UI.
package twitch

import (
	"context"
	"errors"
	"fmt"

	"parasocial/internal/gql"
)

// GQLClient is the minimal GraphQL interface the Twitch service depends on.
type GQLClient interface {
	Do(context.Context, gql.Request, any) error
}

// Service exposes the Twitch lookups the current app needs.
type Service struct {
	GQL GQLClient
}

// Viewer is the authenticated Twitch account.
type Viewer struct {
	ID    string
	Login string
}

// Channel is the resolved Twitch channel identity for one streamer login.
type Channel struct {
	ID    string
	Login string
}

// StreamInfo describes whether a channel is live.
type StreamInfo struct {
	Online bool
}

// PlaybackToken carries the Twitch playback token/signature pair for live HLS access.
type PlaybackToken struct {
	Signature string
	Value     string
}

// StreamerStatus describes one row's resolution state in the UI.
type StreamerStatus string

const (
	StreamerLoading StreamerStatus = "loading"
	StreamerReady   StreamerStatus = "ready"
	StreamerError   StreamerStatus = "error"
)

// StreamerEntry is the UI-facing state for one configured streamer.
type StreamerEntry struct {
	ConfigLogin string
	Login       string
	ChannelID   string
	Live        bool
	Status      StreamerStatus
	Error       string
}

// ErrStreamerNotFound is returned when Twitch has no channel for the requested login.
var ErrStreamerNotFound = errors.New("streamer does not exist")

// LoadingStreamerEntries seeds UI state from the normalized config logins.
func LoadingStreamerEntries(logins []string) []StreamerEntry {
	entries := make([]StreamerEntry, 0, len(logins))
	for _, login := range logins {
		entries = append(entries, StreamerEntry{
			ConfigLogin: login,
			Status:      StreamerLoading,
		})
	}
	return entries
}

// CurrentUser resolves the authenticated viewer through Twitch GraphQL.
func (s *Service) CurrentUser(ctx context.Context) (*Viewer, error) {
	var data struct {
		CurrentUser *struct {
			ID    string `json:"id"`
			Login string `json:"login"`
		} `json:"currentUser"`
	}
	if err := s.GQL.Do(ctx, gql.CurrentUser(), &data); err != nil {
		return nil, err
	}
	if data.CurrentUser == nil || data.CurrentUser.ID == "" || data.CurrentUser.Login == "" {
		return nil, fmt.Errorf("current user response missing id or login")
	}
	return &Viewer{
		ID:    data.CurrentUser.ID,
		Login: data.CurrentUser.Login,
	}, nil
}

// ResolveStreamer resolves a configured streamer login into canonical Twitch identity.
func (s *Service) ResolveStreamer(ctx context.Context, login string) (*Channel, error) {
	var data struct {
		User *struct {
			ID    string `json:"id"`
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := s.GQL.Do(ctx, gql.GetIDFromLogin(login), &data); err != nil {
		return nil, err
	}
	if data.User == nil || data.User.ID == "" {
		return nil, fmt.Errorf("%w: %s", ErrStreamerNotFound, login)
	}
	resolvedLogin := data.User.Login
	if resolvedLogin == "" {
		resolvedLogin = login
	}
	return &Channel{ID: data.User.ID, Login: resolvedLogin}, nil
}

// StreamInfo resolves whether the given channel ID is currently live.
func (s *Service) StreamInfo(ctx context.Context, channelID string) (*StreamInfo, error) {
	var data struct {
		User *struct {
			Stream *struct {
				ID string `json:"id"`
			} `json:"stream"`
		} `json:"user"`
	}
	if err := s.GQL.Do(ctx, gql.WithIsStreamLiveQuery(channelID), &data); err != nil {
		return nil, err
	}
	if data.User == nil {
		return nil, fmt.Errorf("stream info missing user for channel %s", channelID)
	}
	if data.User.Stream == nil {
		return &StreamInfo{Online: false}, nil
	}
	return &StreamInfo{Online: true}, nil
}

// PlaybackAccessToken fetches the Twitch playback token needed to access a live stream.
func (s *Service) PlaybackAccessToken(ctx context.Context, login string) (*PlaybackToken, error) {
	var data struct {
		StreamPlaybackAccessToken *struct {
			Signature string `json:"signature"`
			Value     string `json:"value"`
		} `json:"streamPlaybackAccessToken"`
	}
	if err := s.GQL.Do(ctx, gql.PlaybackAccessToken(login), &data); err != nil {
		return nil, err
	}
	if data.StreamPlaybackAccessToken == nil || data.StreamPlaybackAccessToken.Signature == "" || data.StreamPlaybackAccessToken.Value == "" {
		return nil, fmt.Errorf("playback access token missing signature or value for %s", login)
	}
	return &PlaybackToken{
		Signature: data.StreamPlaybackAccessToken.Signature,
		Value:     data.StreamPlaybackAccessToken.Value,
	}, nil
}
