// operations.go defines the minimal Twitch GraphQL operations used by the app.
// It builds either persisted-query or inline query requests for viewer identity
// and streamer login resolution without exposing raw payload assembly to callers.
package gql

import "strings"

// persistedQuery stores the persisted-query metadata Twitch expects.
type persistedQuery struct {
	Version    int    `json:"version"`
	SHA256Hash string `json:"sha256Hash"`
}

// extensions holds the GraphQL extensions block for one request.
type extensions struct {
	PersistedQuery *persistedQuery `json:"persistedQuery,omitempty"`
}

// Request is one Twitch GraphQL operation.
type Request struct {
	OperationName string         `json:"operationName,omitempty"`
	Query         string         `json:"query,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
	Extensions    extensions     `json:"extensions,omitempty"`
}

// operation builds a persisted-query request with the supplied variables.
func operation(name, hash string, variables map[string]any) Request {
	return Request{
		OperationName: name,
		Variables:     variables,
		Extensions: extensions{
			PersistedQuery: &persistedQuery{
				Version:    1,
				SHA256Hash: hash,
			},
		},
	}
}

// queryOperation builds an inline-query request with the supplied variables.
func queryOperation(name, query string, variables map[string]any) Request {
	return Request{
		OperationName: name,
		Query:         query,
		Variables:     variables,
	}
}

// operationLabel returns a readable operation name for logs and errors.
func (r Request) operationLabel() string {
	if r.OperationName != "" {
		return r.OperationName
	}
	if r.Query != "" {
		return "anonymous"
	}
	return "unknown"
}

// CurrentUser fetches the canonical identity for the authenticated viewer.
func CurrentUser() Request {
	return queryOperation("CurrentUser", "query CurrentUser { currentUser { id login } }", nil)
}

// GetIDFromLogin resolves one login into a Twitch user record using Twitch's persisted-query hash.
func GetIDFromLogin(login string) Request {
	return operation("GetIDFromLogin", "94e82a7b1e3c21e186daa73ee2afc4b8f23bade1fbbff6fe8ac133f50a2f58ca", map[string]any{
		"login": login,
	})
}

// PlaybackAccessToken fetches the HLS playback token Twitch issues for a live channel.
func PlaybackAccessToken(login string) Request {
	return operation("PlaybackAccessToken", "3093517e37e4f4cb48906155bcd894150aef92617939236d2508f3375ab732ce", map[string]any{
		"login":      login,
		"isLive":     true,
		"isVod":      false,
		"vodID":      "",
		"playerType": "site",
	})
}

// WithIsStreamLiveQuery fetches live stream metadata for one channel by ID.
func WithIsStreamLiveQuery(channelID string) Request {
	return operation("WithIsStreamLiveQuery", "04e46329a6786ff3a81c01c50bfa5d725902507a0deb83b0edbf7abe7a3716ea", map[string]any{
		"id": channelID,
	})
}

// ChannelPointsContext fetches channel points state for one streamer login.
func ChannelPointsContext(login string) Request {
	return operation("ChannelPointsContext", "1530a003a7d374b0380b79db0be0534f30ff46e61cffa2bc0e2468a909fbc024", map[string]any{
		"channelLogin": login,
	})
}

// WatchStreak fetches the authenticated viewer's watch-streak milestone for one streamer.
func WatchStreak(login string) Request {
	return queryOperation(
		"WatchStreak",
		`query WatchStreak($login: String!) { user(login: $login) { channel { self { viewerMilestones { id category value } } } } }`,
		map[string]any{"login": login},
	)
}

// ClaimCommunityPoints claims an available channel points bonus chest.
func ClaimCommunityPoints(channelID, claimID string) Request {
	return operation("ClaimCommunityPoints", "46aaeebe02c99afdf4fc97c7c0cba964124bf6b0af229395f1f6d1feed05b3d0", map[string]any{
		"input": map[string]any{
			"channelID": channelID,
			"claimID":   claimID,
		},
	})
}

// VideoPlayerStreamInfoOverlayChannel fetches stream metadata used for watch telemetry.
func VideoPlayerStreamInfoOverlayChannel(login string) Request {
	return operation("VideoPlayerStreamInfoOverlayChannel", "198492e0857f6aedead9665c81c5a06d67b25b58034649687124083ff288597d", map[string]any{
		"channel": login,
	})
}

// HLSMasterPlaylistURL builds the usher URL Twitch uses for live playback.
func HLSMasterPlaylistURL(login, signature, token string) string {
	var builder strings.Builder
	builder.Grow(len(login) + len(signature) + len(token) + 80)
	builder.WriteString("https://usher.ttvnw.net/api/channel/hls/")
	builder.WriteString(login)
	builder.WriteString(".m3u8?player=twitchweb&allow_source=true&type=any&p=1&sig=")
	builder.WriteString(signature)
	builder.WriteString("&token=")
	builder.WriteString(token)
	return builder.String()
}
