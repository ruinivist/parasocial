// auth.go implements the Twitch TV device login flow and cached token validation.
// It owns the HTTP interaction with Twitch OAuth endpoints, the polling loop,
// and the status messages consumed by the TUI while authentication is in progress.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ClientID = "ue6666qo983tsx6so1t0vnawi233wa"

	deviceIDLength  = 32
	contentTypeForm = "application/x-www-form-urlencoded"
	activateURL     = "https://www.twitch.tv/activate"
	pollGrantType   = "urn:ietf:params:oauth:grant-type:device_code"

	tvOrigin    = "https://android.tv.twitch.tv"
	tvReferer   = "https://android.tv.twitch.tv/"
	tvUserAgent = "Mozilla/5.0 (Linux; Android 7.1; Smart Box C1) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/108.0.0.0 Safari/537.36"
)

var RequiredScopes = []string{
	"channel_read",
	"chat:read",
	"user_blocks_edit",
	"user_blocks_read",
	"user_follows_edit",
	"user_read",
}

var DefaultEndpoints = Endpoints{
	Device:   "https://id.twitch.tv/oauth2/device",
	Token:    "https://id.twitch.tv/oauth2/token",
	Validate: "https://id.twitch.tv/oauth2/validate",
}

// Endpoints groups the Twitch OAuth URLs used by the TV device flow client.
type Endpoints struct {
	Device   string
	Token    string
	Validate string
}

// Client performs token validation and TV device-flow authentication against Twitch.
type Client struct {
	HTTPClient *http.Client
	Endpoints  Endpoints
	Now        func() time.Time
	Sleep      func(context.Context, time.Duration) error
}

// deviceCodeResponse models the device-code payload returned by Twitch.
type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
}

// tokenResponse models the token payload returned after device authorization succeeds.
type tokenResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	Scope        []string `json:"scope"`
	TokenType    string   `json:"token_type"`
	ExpiresIn    int      `json:"expires_in"`
}

// validationResponse models the token introspection data returned by Twitch validate.
type validationResponse struct {
	ClientID  string   `json:"client_id"`
	Login     string   `json:"login"`
	Scopes    []string `json:"scopes"`
	UserID    string   `json:"user_id"`
	ExpiresIn int      `json:"expires_in"`
}

// oauthErrorResponse captures structured OAuth errors returned by Twitch endpoints.
type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Message          string `json:"message"`
	Status           int    `json:"status"`
}

type StatusFunc func(string)

// StatusError reports a non-200 HTTP response together with its response body.
type StatusError struct {
	StatusCode int
	Body       string
}

// Error formats the HTTP status failure in a way that preserves the response body.
func (e *StatusError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("unexpected status %d", e.StatusCode)
	}
	return fmt.Sprintf("unexpected status %d: %s", e.StatusCode, body)
}

// NewClient constructs an auth client with default endpoints and timing hooks.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		HTTPClient: httpClient,
		Endpoints:  DefaultEndpoints,
		Now:        time.Now,
		Sleep:      sleepContext,
	}
}

// ReuseAuth validates a saved auth bundle and returns it only when it is still usable.
func (c *Client) ReuseAuth(ctx context.Context, path string) (*State, error) {
	state, err := LoadState(path)
	if err != nil {
		return nil, fmt.Errorf("load auth state: %w", err)
	}
	if state == nil || state.AccessToken == "" {
		return nil, nil
	}
	if state.DeviceID == "" {
		deviceID, err := randomAlphaNumeric(deviceIDLength)
		if err != nil {
			return nil, fmt.Errorf("create device ID: %w", err)
		}
		state.DeviceID = deviceID
	}

	validation, err := c.ValidateToken(ctx, state.AccessToken)
	if err != nil || !hasAllScopes(validation.Scopes, RequiredScopes) {
		return nil, nil
	}

	state.applyValidation(validation, c.now())
	if err := SaveState(path, state); err != nil {
		return nil, fmt.Errorf("save validated auth state: %w", err)
	}
	return state, nil
}

// EnsureAuth reuses cached auth when possible and otherwise runs the TV device flow.
func (c *Client) EnsureAuth(ctx context.Context, path string, status StatusFunc) (*State, error) {
	state, err := LoadState(path)
	if err != nil {
		return nil, fmt.Errorf("load auth state: %w", err)
	}
	if state == nil {
		state = &State{}
	}
	if state.DeviceID == "" {
		deviceID, err := randomAlphaNumeric(deviceIDLength)
		if err != nil {
			return nil, fmt.Errorf("create device ID: %w", err)
		}
		state.DeviceID = deviceID
	}

	if state.AccessToken != "" {
		c.status(status, "Validating cached token from %s", path)
		validation, err := c.ValidateToken(ctx, state.AccessToken)
		if err == nil && hasAllScopes(validation.Scopes, RequiredScopes) {
			state.applyValidation(validation, c.now())
			if err := SaveState(path, state); err != nil {
				return nil, fmt.Errorf("save validated auth state: %w", err)
			}
			c.status(status, "Cached token is valid; authenticated as %s", state.Login)
			return state, nil
		}
		if err != nil {
			c.status(status, "Cached token could not be reused: %v", err)
		} else {
			c.status(status, "Cached token is missing required scopes; activation is required")
		}
	}

	tokenResp, err := c.activate(ctx, state.DeviceID, status)
	if err != nil {
		return nil, err
	}

	validation, err := c.ValidateToken(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("validate new token: %w", err)
	}
	if !hasAllScopes(validation.Scopes, RequiredScopes) {
		return nil, fmt.Errorf("new token is missing required scopes")
	}

	state.AccessToken = tokenResp.AccessToken
	state.RefreshToken = tokenResp.RefreshToken
	state.TokenType = tokenResp.TokenType
	if state.TokenType == "" {
		state.TokenType = "bearer"
	}
	state.Scopes = tokenResp.Scope
	state.ExpiresIn = tokenResp.ExpiresIn
	state.applyValidation(validation, c.now())

	if err := SaveState(path, state); err != nil {
		return nil, fmt.Errorf("save auth state: %w", err)
	}
	c.status(status, "Saved auth state to %s", path)
	c.status(status, "Authenticated as %s", state.Login)
	return state, nil
}

// ValidateToken asks Twitch whether an OAuth access token is still valid.
func (c *Client) ValidateToken(ctx context.Context, accessToken string) (*validationResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoints().Validate, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "OAuth "+accessToken)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var validation validationResponse
	if err := json.Unmarshal(body, &validation); err != nil {
		return nil, fmt.Errorf("parse validation response: %w", err)
	}
	if validation.Login == "" {
		return nil, errors.New("validation response missing login")
	}
	if validation.UserID == "" {
		return nil, errors.New("validation response missing user_id")
	}
	if validation.ClientID == "" {
		return nil, errors.New("validation response missing client_id")
	}
	return &validation, nil
}

// activate requests a device code and waits for the user to authorize it.
func (c *Client) activate(ctx context.Context, deviceID string, status StatusFunc) (*tokenResponse, error) {
	deviceResp, err := c.requestDeviceCode(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}

	interval := time.Duration(deviceResp.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := c.now().Add(time.Duration(deviceResp.ExpiresIn) * time.Second)

	c.status(status, "== Twitch activation ==")
	c.status(status, "Open page: %s", deviceResp.VerificationURI)
	c.status(status, "User code: %s", deviceResp.UserCode)
	c.status(status, "Polling interval: %s", interval)
	c.status(status, "Expires at: %s", deadline.Format(time.RFC3339))

	return c.pollForToken(ctx, deviceID, deviceResp.DeviceCode, interval, deadline, status)
}

// requestDeviceCode starts the Twitch TV device flow and returns the activation instructions.
func (c *Client) requestDeviceCode(ctx context.Context, deviceID string) (*deviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", ClientID)
	form.Set("scopes", strings.Join(RequiredScopes, " "))

	body, statusCode, err := c.postForm(ctx, c.endpoints().Device, deviceID, form)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from device endpoint: %s", statusCode, strings.TrimSpace(string(body)))
	}

	var resp deviceCodeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse device-code response: %w", err)
	}
	if resp.VerificationURI == "" {
		resp.VerificationURI = activateURL
	}
	if err := validateDeviceCodeResponse(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// pollForToken repeatedly checks the token endpoint until the device flow resolves.
func (c *Client) pollForToken(ctx context.Context, deviceID, deviceCode string, interval time.Duration, deadline time.Time, status StatusFunc) (*tokenResponse, error) {
	attempt := 1
	for {
		if !c.now().Before(deadline) {
			return nil, fmt.Errorf("device code expired at %s before authorization completed", deadline.Format(time.RFC3339))
		}

		remaining := deadline.Sub(c.now()).Round(time.Second)
		c.status(status, "[poll %d] waiting %s before token request (%s remaining)", attempt, interval, remaining)
		if err := c.sleep(ctx, interval); err != nil {
			return nil, err
		}

		form := url.Values{}
		form.Set("client_id", ClientID)
		form.Set("device_code", deviceCode)
		form.Set("grant_type", pollGrantType)

		body, statusCode, err := c.postForm(ctx, c.endpoints().Token, deviceID, form)
		if err != nil {
			return nil, fmt.Errorf("poll request failed on attempt %d: %w", attempt, err)
		}

		if statusCode == http.StatusOK {
			var resp tokenResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return nil, fmt.Errorf("parse token response: %w", err)
			}
			if resp.AccessToken == "" {
				return nil, errors.New("token response missing access_token")
			}
			c.status(status, "[poll %d] authorization succeeded", attempt)
			return &resp, nil
		}

		oauthErr := parseOAuthError(body)
		switch oauthErr.Error {
		case "authorization_pending", "":
			c.status(status, "[poll %d] authorization pending (status %d)", attempt, statusCode)
		case "slow_down":
			interval += 5 * time.Second
			c.status(status, "[poll %d] Twitch requested slow_down; new interval %s", attempt, interval)
		case "access_denied":
			return nil, fmt.Errorf("authorization denied by user: %s", oauthErr.summary())
		case "expired_token", "invalid_device_code":
			return nil, fmt.Errorf("device flow expired or invalid: %s", oauthErr.summary())
		default:
			return nil, fmt.Errorf("token endpoint returned status %d: %s", statusCode, oauthErr.summary())
		}

		attempt++
	}
}

// postForm sends a form-encoded Twitch OAuth request with the TV client headers attached.
func (c *Client) postForm(ctx context.Context, endpoint, deviceID string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	for key, value := range defaultOAuthHeaders(deviceID) {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", contentTypeForm)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// validateDeviceCodeResponse rejects incomplete or unusable device-code payloads.
func validateDeviceCodeResponse(resp *deviceCodeResponse) error {
	switch {
	case resp.DeviceCode == "":
		return errors.New("device_code is missing")
	case resp.UserCode == "":
		return errors.New("user_code is missing")
	case resp.VerificationURI == "":
		return errors.New("verification_uri is missing")
	case resp.ExpiresIn <= 0:
		return errors.New("expires_in must be greater than zero")
	case resp.Interval <= 0:
		return errors.New("interval must be greater than zero")
	default:
		return nil
	}
}

// parseOAuthError decodes a structured OAuth error or falls back to raw response text.
func parseOAuthError(body []byte) oauthErrorResponse {
	var resp oauthErrorResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		resp.Message = strings.TrimSpace(string(body))
	}
	return resp
}

// summary compresses an OAuth error payload into one log-friendly string.
func (r oauthErrorResponse) summary() string {
	parts := []string{}
	if r.Error != "" {
		parts = append(parts, fmt.Sprintf("error=%s", r.Error))
	}
	if r.ErrorDescription != "" {
		parts = append(parts, fmt.Sprintf("description=%s", r.ErrorDescription))
	}
	if r.Message != "" {
		parts = append(parts, fmt.Sprintf("message=%s", r.Message))
	}
	if r.Status != 0 {
		parts = append(parts, fmt.Sprintf("status=%d", r.Status))
	}
	if len(parts) == 0 {
		return "no JSON error payload"
	}
	return strings.Join(parts, ", ")
}

// defaultOAuthHeaders builds the Twitch TV request headers used for auth requests.
func defaultOAuthHeaders(deviceID string) map[string]string {
	return map[string]string{
		"Accept":          "application/json",
		"Accept-Language": "en-US",
		"Cache-Control":   "no-cache",
		"Client-Id":       ClientID,
		"Origin":          tvOrigin,
		"Pragma":          "no-cache",
		"Referer":         tvReferer,
		"User-Agent":      TVUserAgent(),
		"X-Device-Id":     deviceID,
	}
}

// TVUserAgent returns the Twitch TV user agent used to impersonate the device client.
func TVUserAgent() string {
	return tvUserAgent
}

// applyValidation copies validated account metadata back onto the persisted auth state.
func (s *State) applyValidation(validation *validationResponse, validatedAt time.Time) {
	s.Login = validation.Login
	s.UserID = validation.UserID
	s.ClientID = validation.ClientID
	s.Scopes = validation.Scopes
	s.ExpiresIn = validation.ExpiresIn
	s.ValidatedAt = validatedAt
	s.Cookies = s.persistedCookies()
}

// hasScope reports whether the token scopes include one exact scope string.
func hasScope(scopes []string, scope string) bool {
	for _, candidate := range scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// hasAllScopes reports whether the token satisfies the full required scope set.
func hasAllScopes(scopes []string, required []string) bool {
	for _, scope := range required {
		if !hasScope(scopes, scope) {
			return false
		}
	}
	return true
}

// randomAlphaNumeric creates a device identifier using Twitch-safe alphanumeric characters.
func randomAlphaNumeric(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var builder strings.Builder
	builder.Grow(length)

	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		builder.WriteByte(alphabet[n.Int64()])
	}
	return builder.String(), nil
}

// sleepContext waits for a duration unless the surrounding context is canceled first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// httpClient returns the configured HTTP client or falls back to the default client.
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// endpoints returns the configured endpoint set or the package defaults.
func (c *Client) endpoints() Endpoints {
	if c.Endpoints == (Endpoints{}) {
		return DefaultEndpoints
	}
	return c.Endpoints
}

// now returns the current time using the injected clock when one is configured.
func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// sleep delegates waiting to the injected hook when tests need deterministic timing.
func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	return sleepContext(ctx, d)
}

// status formats and emits one auth progress line when a status sink is attached.
func (c *Client) status(status StatusFunc, format string, args ...any) {
	if status == nil {
		return
	}
	status(fmt.Sprintf(format, args...))
}
