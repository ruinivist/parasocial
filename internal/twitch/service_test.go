package twitch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"parasocial/internal/gql"
)

type fakeGQL struct {
	requests []gql.Request
	data     map[string]string
}

func (f *fakeGQL) Do(_ context.Context, request gql.Request, out any) error {
	f.requests = append(f.requests, request)
	return json.Unmarshal([]byte(f.data[request.OperationName]), out)
}

func TestCurrentUser(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"CurrentUser": `{"currentUser":{"id":"7","login":"viewer"}}`,
	}}
	service := &Service{GQL: client}
	viewer, err := service.CurrentUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if viewer.ID != "7" || viewer.Login != "viewer" {
		t.Fatalf("viewer = %#v", viewer)
	}
}

func TestResolveStreamer(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"GetIDFromLogin": `{"user":{"id":"123","login":"streamer"}}`,
	}}
	service := &Service{GQL: client}
	channel, err := service.ResolveStreamer(context.Background(), "streamer")
	if err != nil {
		t.Fatal(err)
	}
	if channel.ID != "123" || channel.Login != "streamer" {
		t.Fatalf("channel = %#v", channel)
	}
}

func TestResolveStreamerNotFound(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"GetIDFromLogin": `{"user":null}`,
	}}
	service := &Service{GQL: client}
	_, err := service.ResolveStreamer(context.Background(), "missing")
	if !errors.Is(err, ErrStreamerNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestStreamInfoOffline(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"WithIsStreamLiveQuery": `{"user":{"stream":null}}`,
	}}
	service := &Service{GQL: client}
	info, err := service.StreamInfo(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if info.Online {
		t.Fatalf("info = %#v, want offline", info)
	}
}

func TestStreamInfoOnline(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"WithIsStreamLiveQuery": `{"user":{"stream":{"id":"broadcast"}}}`,
	}}
	service := &Service{GQL: client}
	info, err := service.StreamInfo(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Online {
		t.Fatalf("info = %#v, want online", info)
	}
}

func TestPlaybackAccessToken(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"PlaybackAccessToken": `{"streamPlaybackAccessToken":{"signature":"sig","value":"token"}}`,
	}}
	service := &Service{GQL: client}
	token, err := service.PlaybackAccessToken(context.Background(), "streamer")
	if err != nil {
		t.Fatal(err)
	}
	if token.Signature != "sig" || token.Value != "token" {
		t.Fatalf("token = %#v", token)
	}
}

func TestPlaybackAccessTokenMissingFields(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"PlaybackAccessToken": `{"streamPlaybackAccessToken":{"signature":"sig"}}`,
	}}
	service := &Service{GQL: client}
	_, err := service.PlaybackAccessToken(context.Background(), "streamer")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadChannelPointsContext(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"ChannelPointsContext": `{"community":{"channel":{"self":{"communityPoints":{"balance":1234,"availableClaim":{"id":"claim-1"}}}}}}`,
	}}
	service := &Service{GQL: client}
	context, err := service.LoadChannelPointsContext(context.Background(), "streamer")
	if err != nil {
		t.Fatal(err)
	}
	if context.Balance != 1234 || context.ClaimID != "claim-1" {
		t.Fatalf("context = %#v", context)
	}
}

func TestClaimCommunityPoints(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"ClaimCommunityPoints": `{"claimCommunityPoints":{"id":"claim-1"}}`,
	}}
	service := &Service{GQL: client}
	if err := service.ClaimCommunityPoints(context.Background(), "7", "claim-1"); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	input := client.requests[0].Variables["input"].(map[string]any)
	if input["channelID"] != "7" || input["claimID"] != "claim-1" {
		t.Fatalf("input = %#v", input)
	}
}

func TestStreamMetadata(t *testing.T) {
	t.Parallel()

	client := &fakeGQL{data: map[string]string{
		"VideoPlayerStreamInfoOverlayChannel": `{"user":{"broadcastSettings":{"title":" Live title ","game":{"id":"99","name":"game-name","displayName":"Game Name"}},"stream":{"id":"broadcast","viewersCount":77,"tags":[{"id":"1","localizedName":"English"}]}}}`,
	}}
	service := &Service{GQL: client}
	metadata, err := service.StreamMetadata(context.Background(), "streamer")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.BroadcastID != "broadcast" || metadata.Title != "Live title" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.Game == nil || metadata.Game.Name != "game-name" {
		t.Fatalf("game = %#v", metadata.Game)
	}
	if len(metadata.Tags) != 1 || metadata.Tags[0].LocalizedName != "English" {
		t.Fatalf("tags = %#v", metadata.Tags)
	}
}

func TestFetchSpadeURL(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/streamer":
			_, _ = io.WriteString(w, `<script src="https://assets.twitch.tv/config/settings.abc.js"></script>`)
		case "/config/settings.abc.js":
			_, _ = io.WriteString(w, `{"spade_url":"https:\/\/spade.example.test\/collect"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalPageURL := defaultPageURL
	defaultPageURL = server.URL + "/"
	defer func() { defaultPageURL = originalPageURL }()

	service := &Service{HTTPClient: rewriteClient(server.URL, server.Client())}
	spadeURL, err := service.FetchSpadeURL(context.Background(), "streamer")
	if err != nil {
		t.Fatal(err)
	}
	if spadeURL != "https://spade.example.test/collect" {
		t.Fatalf("spadeURL = %q", spadeURL)
	}
}

func TestTouchPlayback(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/channel/hls/streamer.m3u8":
			_, _ = io.WriteString(w, "#EXTM3U\n"+server.URL+"/variant.m3u8\n")
		case "/variant.m3u8":
			_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:1,\n"+server.URL+"/chunk.ts\n")
		case "/chunk.ts":
			if r.Method != http.MethodHead {
				t.Fatalf("method = %s, want HEAD", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := &Service{HTTPClient: rewriteClient(server.URL, server.Client())}
	if err := service.TouchPlayback(context.Background(), "streamer", &PlaybackToken{Signature: "sig", Value: "token"}); err != nil {
		t.Fatal(err)
	}
}

func TestSendMinuteWatched(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		encoded := strings.TrimPrefix(string(body), "data=")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(decoded), `"event":"minute-watched"`) {
			t.Fatalf("payload = %s", decoded)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	service := &Service{HTTPClient: server.Client()}
	payload := BuildMinuteWatchedPayload("viewer", "channel", "streamer", &StreamMetadata{
		BroadcastID: "broadcast",
		Game:        &Game{ID: "game-id", Name: "game-name"},
	})
	if err := service.SendMinuteWatched(context.Background(), server.URL, payload); err != nil {
		t.Fatal(err)
	}
}

func rewriteClient(baseURL string, client *http.Client) *http.Client {
	copy := *client
	copy.Transport = rewriteTransport{baseURL: baseURL, next: client.Transport}
	return &copy
}

type rewriteTransport struct {
	baseURL string
	next    http.RoundTripper
}

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasPrefix(r.URL.Host, "usher.ttvnw.net") || strings.HasPrefix(r.URL.Host, "assets.twitch.tv") || strings.HasPrefix(r.URL.Host, "static.twitchcdn.net") {
		target, err := http.NewRequest(http.MethodGet, t.baseURL, nil)
		if err != nil {
			return nil, err
		}
		r.URL.Scheme = target.URL.Scheme
		r.URL.Host = target.URL.Host
	}
	if next := t.next; next != nil {
		return next.RoundTrip(r)
	}
	return http.DefaultTransport.RoundTrip(r)
}
