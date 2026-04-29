package gql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoSendsHeadersAndDecodesData(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "OAuth token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Client-Id") != "client" {
			t.Fatalf("Client-Id = %q", r.Header.Get("Client-Id"))
		}
		if r.Header.Get("X-Device-Id") != "device" {
			t.Fatalf("X-Device-Id = %q", r.Header.Get("X-Device-Id"))
		}
		if r.Header.Get("User-Agent") != "agent" {
			t.Fatalf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.OperationName != "GetIDFromLogin" {
			t.Fatalf("operation = %q", req.OperationName)
		}
		if req.Variables["login"] != "streamer" {
			t.Fatalf("login variable = %#v", req.Variables["login"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer server.Close()

	client := &Client{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
		Session: Session{
			AccessToken: "token",
			ClientID:    "client",
			DeviceID:    "device",
			UserAgent:   "agent",
		},
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.Do(context.Background(), GetIDFromLogin("streamer"), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatal("expected decoded data")
	}
}

func TestDoSupportsInlineQueries(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.OperationName != "CurrentUser" {
			t.Fatalf("operation = %q", req.OperationName)
		}
		if !strings.Contains(req.Query, "currentUser") {
			t.Fatalf("query = %q", req.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"currentUser":{"id":"1","login":"viewer"}}}`))
	}))
	defer server.Close()

	client := &Client{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
		Session: Session{
			AccessToken: "token",
			ClientID:    "client",
			DeviceID:    "device",
			UserAgent:   "agent",
		},
	}
	var out struct {
		CurrentUser struct {
			ID    string `json:"id"`
			Login string `json:"login"`
		} `json:"currentUser"`
	}
	if err := client.Do(context.Background(), CurrentUser(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CurrentUser.Login != "viewer" {
		t.Fatalf("login = %q", out.CurrentUser.Login)
	}
}

func TestDoSurfacesGraphQLErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"bad auth"}]}`))
	}))
	defer server.Close()

	client := &Client{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
		Session: Session{
			AccessToken: "token",
			ClientID:    "client",
			DeviceID:    "device",
			UserAgent:   "agent",
		},
	}
	if err := client.Do(context.Background(), CurrentUser(), &struct{}{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestPersistedOperationHashes(t *testing.T) {
	t.Parallel()

	req := GetIDFromLogin("streamer")
	if req.OperationName != "GetIDFromLogin" {
		t.Fatalf("operation = %q", req.OperationName)
	}
	if req.Extensions.PersistedQuery == nil {
		t.Fatal("expected persisted query metadata")
	}
	if got := req.Extensions.PersistedQuery.SHA256Hash; got != "94e82a7b1e3c21e186daa73ee2afc4b8f23bade1fbbff6fe8ac133f50a2f58ca" {
		t.Fatalf("hash = %q", got)
	}

	channelPoints := ChannelPointsContext("streamer")
	if got := channelPoints.Extensions.PersistedQuery.SHA256Hash; got != "1530a003a7d374b0380b79db0be0534f30ff46e61cffa2bc0e2468a909fbc024" {
		t.Fatalf("channel points hash = %q", got)
	}

	claim := ClaimCommunityPoints("7", "claim-1")
	if got := claim.Extensions.PersistedQuery.SHA256Hash; got != "46aaeebe02c99afdf4fc97c7c0cba964124bf6b0af229395f1f6d1feed05b3d0" {
		t.Fatalf("claim hash = %q", got)
	}

	streamInfo := VideoPlayerStreamInfoOverlayChannel("streamer")
	if got := streamInfo.Extensions.PersistedQuery.SHA256Hash; got != "198492e0857f6aedead9665c81c5a06d67b25b58034649687124083ff288597d" {
		t.Fatalf("stream info hash = %q", got)
	}
}
