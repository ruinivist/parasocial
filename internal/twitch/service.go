// service.go defines the Twitch domain lookups the rewritten app currently needs.
// It wraps the lower-level GraphQL client with small typed methods for the viewer
// identity and configured streamer resolution used to populate the terminal UI.
package twitch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"parasocial/internal/gql"
)

const (
	browserUserAgent   = "Mozilla/5.0 (X11; Linux x86_64; rv:85.0) Gecko/20100101 Firefox/85.0"
	spadeSettingsRegex = `(https://static\.twitchcdn\.net/config/settings.*?js|https://assets\.twitch\.tv/config/settings.*?\.js)`
	spadeURLRegex      = `"spade_url":"(.*?)"`
)

var (
	defaultPageURL       = "https://www.twitch.tv/"
	settingsAssetPattern = regexp.MustCompile(spadeSettingsRegex)
	spadeURLPattern      = regexp.MustCompile(spadeURLRegex)
)

// GQLClient is the minimal GraphQL interface the Twitch service depends on.
type GQLClient interface {
	Do(context.Context, gql.Request, any) error
}

// Service exposes the Twitch lookups the current app needs.
type Service struct {
	GQL        GQLClient
	HTTPClient *http.Client
	Session    gql.Session
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

// ChannelPointsContext describes current balance and any immediately claimable bonus chest.
type ChannelPointsContext struct {
	Balance int
	ClaimID string
}

// Game describes the current Twitch category for a live stream.
type Game struct {
	ID          string
	Name        string
	DisplayName string
}

// StreamMetadata describes the live stream state used for watch telemetry.
type StreamMetadata struct {
	BroadcastID  string
	Title        string
	Game         *Game
	ViewerCount  int
	Tags         []Tag
	ChannelLogin string
}

// Tag is one stream tag entry returned by Twitch.
type Tag struct {
	ID            string
	LocalizedName string
}

// MinuteWatchedPayload is the Spade telemetry body Twitch accepts for simulated viewing.
type MinuteWatchedPayload []minuteWatchedEvent

type minuteWatchedEvent struct {
	Event      string                  `json:"event"`
	Properties minuteWatchedProperties `json:"properties"`
}

type minuteWatchedProperties struct {
	ChannelID   string `json:"channel_id"`
	BroadcastID string `json:"broadcast_id"`
	Player      string `json:"player"`
	UserID      string `json:"user_id"`
	Live        bool   `json:"live"`
	Channel     string `json:"channel"`
	Game        string `json:"game,omitempty"`
	GameID      string `json:"game_id,omitempty"`
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
	ConfigLogin      string
	Login            string
	ChannelID        string
	Live             bool
	Status           StreamerStatus
	Error            string
	WatchStreak      *int
	WatchStreakError string
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

// LoadChannelPointsContext fetches one streamer's current balance and any available bonus claim.
func (s *Service) LoadChannelPointsContext(ctx context.Context, login string) (*ChannelPointsContext, error) {
	var data struct {
		Community *struct {
			Channel *struct {
				Self *struct {
					CommunityPoints *struct {
						Balance        int `json:"balance"`
						AvailableClaim *struct {
							ID string `json:"id"`
						} `json:"availableClaim"`
					} `json:"communityPoints"`
				} `json:"self"`
			} `json:"channel"`
		} `json:"community"`
	}
	if err := s.GQL.Do(ctx, gql.ChannelPointsContext(login), &data); err != nil {
		return nil, err
	}
	if data.Community == nil || data.Community.Channel == nil || data.Community.Channel.Self == nil || data.Community.Channel.Self.CommunityPoints == nil {
		return nil, fmt.Errorf("channel points context missing community points for %s", login)
	}
	context := &ChannelPointsContext{Balance: data.Community.Channel.Self.CommunityPoints.Balance}
	if claim := data.Community.Channel.Self.CommunityPoints.AvailableClaim; claim != nil {
		context.ClaimID = claim.ID
	}
	return context, nil
}

// WatchStreak fetches the authenticated viewer's current watch streak for one streamer.
func (s *Service) WatchStreak(ctx context.Context, login string) (*int, error) {
	var data struct {
		User *struct {
			Channel *struct {
				Self *struct {
					ViewerMilestones []struct {
						ID       string `json:"id"`
						Category string `json:"category"`
						Value    string `json:"value"`
					} `json:"viewerMilestones"`
				} `json:"self"`
			} `json:"channel"`
		} `json:"user"`
	}
	if err := s.GQL.Do(ctx, gql.WatchStreak(login), &data); err != nil {
		return nil, err
	}
	if data.User == nil || data.User.Channel == nil || data.User.Channel.Self == nil {
		return nil, fmt.Errorf("watch streak missing viewer milestones for %s", login)
	}
	for _, milestone := range data.User.Channel.Self.ViewerMilestones {
		if milestone.Category != "WATCH_STREAK" {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSpace(milestone.Value))
		if err != nil {
			return nil, fmt.Errorf("parse watch streak for %s: %w", login, err)
		}
		return &value, nil
	}
	value := 0
	return &value, nil
}

// ClaimCommunityPoints claims one available channel points bonus chest.
func (s *Service) ClaimCommunityPoints(ctx context.Context, channelID, claimID string) error {
	var data struct {
		ClaimCommunityPoints *struct {
			ID string `json:"id"`
		} `json:"claimCommunityPoints"`
	}
	if err := s.GQL.Do(ctx, gql.ClaimCommunityPoints(channelID, claimID), &data); err != nil {
		return err
	}
	if data.ClaimCommunityPoints == nil {
		return fmt.Errorf("claim community points missing response for channel %s", channelID)
	}
	return nil
}

// StreamMetadata fetches live stream metadata used to build minute-watched telemetry.
func (s *Service) StreamMetadata(ctx context.Context, login string) (*StreamMetadata, error) {
	var data struct {
		User *struct {
			BroadcastSettings *struct {
				Title string `json:"title"`
				Game  *struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					DisplayName string `json:"displayName"`
				} `json:"game"`
			} `json:"broadcastSettings"`
			Stream *struct {
				ID           string `json:"id"`
				ViewersCount int    `json:"viewersCount"`
				Tags         []struct {
					ID            string `json:"id"`
					LocalizedName string `json:"localizedName"`
				} `json:"tags"`
			} `json:"stream"`
		} `json:"user"`
	}
	if err := s.GQL.Do(ctx, gql.VideoPlayerStreamInfoOverlayChannel(login), &data); err != nil {
		return nil, err
	}
	if data.User == nil || data.User.Stream == nil {
		return nil, fmt.Errorf("stream metadata missing live stream for %s", login)
	}
	if data.User.BroadcastSettings == nil {
		return nil, fmt.Errorf("stream metadata missing broadcast settings for %s", login)
	}

	metadata := &StreamMetadata{
		BroadcastID:  data.User.Stream.ID,
		Title:        strings.TrimSpace(data.User.BroadcastSettings.Title),
		ViewerCount:  data.User.Stream.ViewersCount,
		ChannelLogin: login,
	}
	if game := data.User.BroadcastSettings.Game; game != nil {
		metadata.Game = &Game{
			ID:          game.ID,
			Name:        game.Name,
			DisplayName: game.DisplayName,
		}
	}
	for _, tag := range data.User.Stream.Tags {
		metadata.Tags = append(metadata.Tags, Tag{
			ID:            tag.ID,
			LocalizedName: tag.LocalizedName,
		})
	}
	if metadata.BroadcastID == "" {
		return nil, fmt.Errorf("stream metadata missing broadcast id for %s", login)
	}
	return metadata, nil
}

// FetchSpadeURL loads the public Twitch page and extracts the Spade telemetry endpoint.
func (s *Service) FetchSpadeURL(ctx context.Context, login string) (string, error) {
	pageReq, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultPageURL+login, nil)
	if err != nil {
		return "", err
	}
	pageReq.Header.Set("User-Agent", browserUserAgent)
	pageResp, err := s.httpClient().Do(pageReq)
	if err != nil {
		return "", fmt.Errorf("fetch streamer page: %w", err)
	}
	defer pageResp.Body.Close()
	pageBody, err := io.ReadAll(pageResp.Body)
	if err != nil {
		return "", fmt.Errorf("read streamer page: %w", err)
	}
	if pageResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch streamer page: status %d", pageResp.StatusCode)
	}
	settingsURL := settingsAssetPattern.FindString(string(pageBody))
	if settingsURL == "" {
		return "", fmt.Errorf("streamer page missing settings asset for %s", login)
	}

	settingsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, settingsURL, nil)
	if err != nil {
		return "", err
	}
	settingsReq.Header.Set("User-Agent", browserUserAgent)
	settingsResp, err := s.httpClient().Do(settingsReq)
	if err != nil {
		return "", fmt.Errorf("fetch settings asset: %w", err)
	}
	defer settingsResp.Body.Close()
	settingsBody, err := io.ReadAll(settingsResp.Body)
	if err != nil {
		return "", fmt.Errorf("read settings asset: %w", err)
	}
	if settingsResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch settings asset: status %d", settingsResp.StatusCode)
	}

	match := spadeURLPattern.FindSubmatch(settingsBody)
	if len(match) < 2 {
		return "", fmt.Errorf("settings asset missing spade_url for %s", login)
	}
	return strings.ReplaceAll(string(match[1]), `\/`, `/`), nil
}

// TouchPlayback walks the HLS playlist chain Twitch expects before telemetry is posted.
func (s *Service) TouchPlayback(ctx context.Context, login string, token *PlaybackToken) error {
	if token == nil || token.Signature == "" || token.Value == "" {
		return errors.New("playback access token is incomplete")
	}
	masterURL := gql.HLSMasterPlaylistURL(login, token.Signature, token.Value)
	variantURL, err := s.fetchPlaylistTarget(ctx, masterURL)
	if err != nil {
		return fmt.Errorf("fetch master playlist: %w", err)
	}
	mediaURL, err := s.fetchPlaylistTarget(ctx, variantURL)
	if err != nil {
		return fmt.Errorf("fetch variant playlist: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, mediaURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", s.requestUserAgent())
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("head media playlist: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("head media playlist: status %d", resp.StatusCode)
	}
	return nil
}

// BuildMinuteWatchedPayload constructs the Spade telemetry payload for one live stream.
func BuildMinuteWatchedPayload(userID, channelID, login string, metadata *StreamMetadata) MinuteWatchedPayload {
	broadcastID := ""
	if metadata != nil {
		broadcastID = metadata.BroadcastID
	}
	payload := MinuteWatchedPayload{
		{
			Event: "minute-watched",
			Properties: minuteWatchedProperties{
				ChannelID:   channelID,
				BroadcastID: broadcastID,
				Player:      "site",
				UserID:      userID,
				Live:        true,
				Channel:     login,
			},
		},
	}
	if metadata != nil && metadata.Game != nil && metadata.Game.Name != "" && metadata.Game.ID != "" {
		payload[0].Properties.Game = metadata.Game.Name
		payload[0].Properties.GameID = metadata.Game.ID
	}
	return payload
}

// Encode returns the form body Twitch expects for Spade telemetry.
func (p MinuteWatchedPayload) Encode() (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(body), nil
}

// SendMinuteWatched posts one minute-watched payload to Twitch's Spade endpoint.
func (s *Service) SendMinuteWatched(ctx context.Context, spadeURL string, payload MinuteWatchedPayload) error {
	encoded, err := payload.Encode()
	if err != nil {
		return fmt.Errorf("encode minute watched payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spadeURL, strings.NewReader("data="+encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", s.requestUserAgent())
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("post minute watched payload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("post minute watched payload: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *Service) fetchPlaylistTarget(ctx context.Context, targetURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", s.requestUserAgent())
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	lines := bytes.Split(body, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(string(lines[i]))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line, nil
	}
	return "", errors.New("playlist contained no media target")
}

func (s *Service) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return http.DefaultClient
}

func (s *Service) requestUserAgent() string {
	if s.Session.UserAgent != "" {
		return s.Session.UserAgent
	}
	return browserUserAgent
}
