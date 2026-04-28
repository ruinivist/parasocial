// client.go defines the authenticated Twitch GraphQL transport layer.
// It owns request encoding, session header wiring, and response decoding
// so higher-level Twitch services can issue typed operations without repeating HTTP logic.
package gql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const DefaultEndpoint = "https://gql.twitch.tv/gql"

// Session carries the authenticated Twitch headers needed for GraphQL requests.
type Session struct {
	AccessToken string
	ClientID    string
	DeviceID    string
	UserAgent   string
}

// Client posts authenticated GraphQL requests to Twitch.
type Client struct {
	HTTPClient *http.Client
	Endpoint   string
	Session    Session
}

// Error is one entry in a GraphQL errors list.
type Error struct {
	Message string `json:"message"`
}

// StatusError reports a non-200 HTTP response together with its response body.
type StatusError struct {
	StatusCode int
	Body       string
}

// Error formats the HTTP status failure in a way that preserves the response body.
func (e *StatusError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("graphql status %d", e.StatusCode)
	}
	return fmt.Sprintf("graphql status %d: %s", e.StatusCode, body)
}

// Do executes one Twitch GraphQL request and decodes its data payload into out.
func (c *Client) Do(ctx context.Context, request Request, out any) error {
	if err := c.Validate(); err != nil {
		return err
	}

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(request); err != nil {
		return fmt.Errorf("encode graphql request %s: %w", request.operationLabel(), err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "OAuth "+c.Session.AccessToken)
	req.Header.Set("Client-Id", c.Session.ClientID)
	req.Header.Set("User-Agent", c.Session.UserAgent)
	req.Header.Set("X-Device-Id", c.Session.DeviceID)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("post graphql %s: %w", request.operationLabel(), err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read graphql %s response: %w", request.operationLabel(), err)
	}
	if resp.StatusCode != http.StatusOK {
		return &StatusError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []Error         `json:"errors,omitempty"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("parse graphql %s response: %w", request.operationLabel(), err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("graphql %s returned errors: %s", request.operationLabel(), formatErrors(envelope.Errors))
	}
	if len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
		return fmt.Errorf("graphql %s response missing data", request.operationLabel())
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode graphql %s data: %w", request.operationLabel(), err)
	}
	return nil
}

// formatErrors joins GraphQL error messages into one readable string.
func formatErrors(errs []Error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err.Message != "" {
			parts = append(parts, err.Message)
		}
	}
	if len(parts) == 0 {
		return "unknown error"
	}
	return strings.Join(parts, "; ")
}

// Validate rejects incomplete session configuration before any request is sent.
func (c *Client) Validate() error {
	switch {
	case c.Session.AccessToken == "":
		return errors.New("missing access token")
	case c.Session.ClientID == "":
		return errors.New("missing client id")
	case c.Session.DeviceID == "":
		return errors.New("missing device id")
	case c.Session.UserAgent == "":
		return errors.New("missing user agent")
	default:
		return nil
	}
}

// httpClient returns the configured HTTP client or the default client.
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// endpoint returns the configured GraphQL endpoint or the default Twitch endpoint.
func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return DefaultEndpoint
}
