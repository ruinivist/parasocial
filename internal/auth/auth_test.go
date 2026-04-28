package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSaveStatePersistsCookieEntries(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	state := &State{
		AccessToken: "token",
		TokenType:   "bearer",
		Scopes:      RequiredScopes,
		Login:       "viewer",
		UserID:      "123",
		ClientID:    ClientID,
		ExpiresIn:   3600,
		DeviceID:    "device",
	}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "token" {
		t.Fatalf("AccessToken = %q", loaded.AccessToken)
	}

	gotNames := []string{}
	for _, cookie := range loaded.Cookies {
		gotNames = append(gotNames, cookie.Name)
	}
	wantNames := []string{"auth-token", "login", "persistent"}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("cookie names = %#v, want %#v", gotNames, wantNames)
	}
}

func TestSaveStateAllowsZeroExpiresIn(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	state := &State{
		AccessToken: "token",
		TokenType:   "bearer",
		Scopes:      RequiredScopes,
		Login:       "viewer",
		UserID:      "123",
		ClientID:    ClientID,
		ExpiresIn:   0,
		DeviceID:    "device",
	}
	if err := SaveState(path, state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExpiresIn != 0 {
		t.Fatalf("ExpiresIn = %d, want 0", loaded.ExpiresIn)
	}
}

func TestLoadStateMissingFileReturnsNil(t *testing.T) {
	t.Parallel()

	state, err := LoadState(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if state != nil {
		t.Fatalf("LoadState() = %#v, want nil", state)
	}
}

func TestLoadStateRejectsPartialAuthBundle(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadState(path)
	if err == nil {
		t.Fatal("LoadState() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "missing token_type") {
		t.Fatalf("error = %q", err)
	}
}

func TestReuseAuthReusesValidCachedToken(t *testing.T) {
	t.Parallel()

	var validateCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/validate" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		validateCalls++
		if got := r.Header.Get("Authorization"); got != "OAuth cached-token" {
			t.Fatalf("Authorization = %q", got)
		}
		writeJSON(t, w, validationResponse{
			ClientID:  ClientID,
			Login:     "viewer",
			UserID:    "123",
			Scopes:    RequiredScopes,
			ExpiresIn: 3600,
		})
	}))
	defer server.Close()

	tokenFile := filepath.Join(t.TempDir(), "cookies.json")
	if err := SaveState(tokenFile, &State{
		AccessToken: "cached-token",
		TokenType:   "bearer",
		Scopes:      RequiredScopes,
		Login:       "viewer",
		UserID:      "123",
		ClientID:    ClientID,
		ExpiresIn:   3600,
		DeviceID:    "device",
	}); err != nil {
		t.Fatal(err)
	}

	client := NewClient(server.Client())
	client.Endpoints = Endpoints{Validate: server.URL + "/validate"}
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	state, err := client.ReuseAuth(context.Background(), tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if validateCalls != 1 {
		t.Fatalf("validateCalls = %d", validateCalls)
	}
	if state == nil || state.Login != "viewer" {
		t.Fatalf("state = %#v", state)
	}
}

func TestEnsureAuthActivatesWhenCachedTokenMissingChatRead(t *testing.T) {
	t.Parallel()

	validateCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/validate":
			validateCalls++
			if validateCalls == 1 {
				writeJSON(t, w, validationResponse{
					ClientID:  ClientID,
					Login:     "viewer",
					UserID:    "123",
					Scopes:    []string{"user:read:email"},
					ExpiresIn: 100,
				})
				return
			}
			if got := r.Header.Get("Authorization"); got != "OAuth new-token" {
				t.Fatalf("Authorization = %q", got)
			}
			writeJSON(t, w, validationResponse{
				ClientID:  ClientID,
				Login:     "viewer",
				UserID:    "123",
				Scopes:    RequiredScopes,
				ExpiresIn: 3600,
			})
		case "/device":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := r.Form.Get("scopes"); got != "channel_read chat:read user_blocks_edit user_blocks_read user_follows_edit user_read" {
				t.Fatalf("scopes = %q", got)
			}
			writeJSON(t, w, deviceCodeResponse{
				DeviceCode:      "device-code",
				ExpiresIn:       60,
				Interval:        1,
				UserCode:        "ABCD-EFGH",
				VerificationURI: "https://www.twitch.tv/activate",
			})
		case "/token":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			if got := values.Get("device_code"); got != "device-code" {
				t.Fatalf("device_code = %q", got)
			}
			writeJSON(t, w, tokenResponse{
				AccessToken:  "new-token",
				RefreshToken: "refresh-token",
				Scope:        RequiredScopes,
				TokenType:    "bearer",
				ExpiresIn:    3600,
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	tokenFile := filepath.Join(t.TempDir(), "cookies.json")
	if err := SaveState(tokenFile, &State{
		AccessToken: "wrong-scope-token",
		TokenType:   "bearer",
		Scopes:      []string{"user:read:email"},
		Login:       "viewer",
		UserID:      "123",
		ClientID:    ClientID,
		ExpiresIn:   3600,
		DeviceID:    "device",
	}); err != nil {
		t.Fatal(err)
	}

	client := NewClient(server.Client())
	client.Endpoints = Endpoints{
		Device:   server.URL + "/device",
		Token:    server.URL + "/token",
		Validate: server.URL + "/validate",
	}
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	client.Sleep = func(context.Context, time.Duration) error { return nil }

	var logs []string
	state, err := client.EnsureAuth(context.Background(), tokenFile, func(line string) {
		logs = append(logs, line)
	})
	if err != nil {
		t.Fatal(err)
	}
	if validateCalls != 2 {
		t.Fatalf("validateCalls = %d", validateCalls)
	}
	if state.AccessToken != "new-token" {
		t.Fatalf("AccessToken = %q", state.AccessToken)
	}
	if !containsLine(logs, "User code: ABCD-EFGH") {
		t.Fatalf("logs = %#v", logs)
	}
}

func TestEnsureAuthReportsAccessDenied(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			writeJSON(t, w, deviceCodeResponse{
				DeviceCode:      "device-code",
				ExpiresIn:       60,
				Interval:        1,
				UserCode:        "ABCD-EFGH",
				VerificationURI: "https://www.twitch.tv/activate",
			})
		case "/token":
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(t, w, oauthErrorResponse{Error: "access_denied", ErrorDescription: "user said no"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.Endpoints = Endpoints{
		Device:   server.URL + "/device",
		Token:    server.URL + "/token",
		Validate: server.URL + "/validate",
	}
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	client.Sleep = func(context.Context, time.Duration) error { return nil }

	_, err := client.EnsureAuth(context.Background(), filepath.Join(t.TempDir(), "cookies.json"), nil)
	if err == nil {
		t.Fatal("EnsureAuth() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "authorization denied by user") {
		t.Fatalf("error = %q", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}
