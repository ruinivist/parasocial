package miner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultPubSubURL  = "wss://pubsub-edge.twitch.tv/v1"
	pingInterval      = 25 * time.Second
	stalePongDeadline = 55 * time.Second
	reconnectDelay    = 5 * time.Second
)

// Event is one parsed Twitch PubSub message relevant to the miner.
type Event struct {
	Topic       string
	MessageType string
	ChannelID   string
	Timestamp   string
	Balance     int
	ClaimID     string
	ReasonCode  string
	TotalPoints int
}

func (e Event) key() string {
	return fmt.Sprintf("%s|%s|%s|%s", e.MessageType, e.Topic, e.ChannelID, e.Timestamp)
}

// Client maintains Twitch PubSub subscriptions and dispatches decoded miner events.
type Client struct {
	ctx      context.Context
	cancel   context.CancelFunc
	onEvent  func(Event)
	url      string
	dialer   *websocket.Dialer
	mu       sync.Mutex
	conn     *websocket.Conn
	viewerID string
	token    string
	topics   []string
	updateCh chan struct{}
	lastPong time.Time
	lastSeen string
}

// NewPubSubClient constructs a PubSub client with the default Twitch endpoint.
func NewPubSubClient(ctx context.Context, onEvent func(Event)) *Client {
	runCtx, cancel := context.WithCancel(ctx)
	client := &Client{
		ctx:      runCtx,
		cancel:   cancel,
		onEvent:  onEvent,
		url:      defaultPubSubURL,
		dialer:   websocket.DefaultDialer,
		updateCh: make(chan struct{}, 1),
		lastPong: time.Now(),
	}
	go client.run()
	return client
}

// Sync updates the desired user and channel topic set.
func (c *Client) Sync(_ context.Context, viewerID, accessToken string, channelIDs []string) error {
	topics := make([]string, 0, len(channelIDs)+1)
	if viewerID != "" {
		topics = append(topics, "community-points-user-v1."+viewerID)
	}
	for _, channelID := range channelIDs {
		if channelID == "" {
			continue
		}
		topics = append(topics, "video-playback-by-id."+channelID)
	}
	sort.Strings(topics)

	c.mu.Lock()
	c.viewerID = viewerID
	c.token = accessToken
	changed := !equalStrings(c.topics, topics)
	c.topics = topics
	conn := c.conn
	c.mu.Unlock()

	if changed && conn != nil {
		_ = conn.Close()
	}
	select {
	case c.updateCh <- struct{}{}:
	default:
	}
	return nil
}

// Close stops the PubSub run loop and closes any active socket.
func (c *Client) Close() error {
	c.cancel()
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (c *Client) run() {
	for {
		topics, token := c.snapshot()
		if len(topics) == 0 || token == "" {
			select {
			case <-c.ctx.Done():
				return
			case <-c.updateCh:
				continue
			}
		}

		if err := c.runConnection(topics, token); err != nil && errors.Is(err, context.Canceled) {
			return
		}

		if err := sleepContext(c.ctx, reconnectDelay); err != nil {
			return
		}
	}
}

func (c *Client) runConnection(topics []string, token string) error {
	conn, _, err := c.dialer.DialContext(c.ctx, c.url, http.Header{
		"User-Agent": []string{"parasocial"},
	})
	if err != nil {
		return err
	}
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.lastPong = time.Now()
	c.lastSeen = ""
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.mu.Unlock()
	}()

	if err := c.listenTopics(conn, topics, token); err != nil {
		return err
	}

	pingErr := make(chan error, 1)
	go c.pingLoop(conn, pingErr)

	for {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case err := <-pingErr:
			return err
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		frame, err := parseFrame(message)
		if err != nil {
			continue
		}
		switch frame.Type {
		case "PONG":
			c.mu.Lock()
			c.lastPong = time.Now()
			c.mu.Unlock()
		case "RECONNECT":
			return nil
		case "MESSAGE":
			if frame.Event.key() == "" {
				continue
			}
			c.mu.Lock()
			duplicate := c.lastSeen == frame.Event.key()
			if !duplicate {
				c.lastSeen = frame.Event.key()
			}
			c.mu.Unlock()
			if !duplicate && c.onEvent != nil {
				c.onEvent(frame.Event)
			}
		}
	}
}

func (c *Client) pingLoop(conn *websocket.Conn, errCh chan<- error) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if err := conn.WriteJSON(map[string]any{"type": "PING"}); err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
			c.mu.Lock()
			stale := time.Since(c.lastPong) > stalePongDeadline
			c.mu.Unlock()
			if stale {
				select {
				case errCh <- errors.New("stale pong from twitch pubsub"):
				default:
				}
				return
			}
		}
	}
}

func (c *Client) listenTopics(conn *websocket.Conn, topics []string, token string) error {
	payload := map[string]any{
		"type":  "LISTEN",
		"nonce": fmt.Sprintf("%d", time.Now().UnixNano()),
		"data": map[string]any{
			"topics": topics,
		},
	}
	if token != "" {
		payload["data"].(map[string]any)["auth_token"] = token
	}
	return conn.WriteJSON(payload)
}

func (c *Client) snapshot() ([]string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	topics := append([]string(nil), c.topics...)
	return topics, c.token
}

type frame struct {
	Type  string
	Error string
	Event Event
}

func parseFrame(message []byte) (*frame, error) {
	var envelope struct {
		Type  string `json:"type"`
		Error string `json:"error"`
		Data  *struct {
			Topic   string `json:"topic"`
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil {
		return nil, err
	}

	result := &frame{Type: envelope.Type, Error: envelope.Error}
	if envelope.Type != "MESSAGE" || envelope.Data == nil {
		return result, nil
	}

	var payload struct {
		Type string `json:"type"`
		Data struct {
			Timestamp string `json:"timestamp"`
			ChannelID string `json:"channel_id"`
			Claim     *struct {
				ID        string `json:"id"`
				ChannelID string `json:"channel_id"`
			} `json:"claim"`
			Balance *struct {
				Balance   int    `json:"balance"`
				ChannelID string `json:"channel_id"`
			} `json:"balance"`
			PointGain *struct {
				ReasonCode  string `json:"reason_code"`
				TotalPoints int    `json:"total_points"`
			} `json:"point_gain"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(envelope.Data.Message), &payload); err != nil {
		return nil, err
	}

	channelID := payload.Data.ChannelID
	if payload.Data.Claim != nil && payload.Data.Claim.ChannelID != "" {
		channelID = payload.Data.Claim.ChannelID
	}
	if payload.Data.Balance != nil && payload.Data.Balance.ChannelID != "" {
		channelID = payload.Data.Balance.ChannelID
	}
	if channelID == "" {
		channelID = topicSuffix(envelope.Data.Topic)
	}

	result.Event = Event{
		Topic:       topicPrefix(envelope.Data.Topic),
		MessageType: payload.Type,
		ChannelID:   channelID,
		Timestamp:   payload.Data.Timestamp,
	}
	if payload.Data.Balance != nil {
		result.Event.Balance = payload.Data.Balance.Balance
	}
	if payload.Data.Claim != nil {
		result.Event.ClaimID = payload.Data.Claim.ID
	}
	if payload.Data.PointGain != nil {
		result.Event.ReasonCode = payload.Data.PointGain.ReasonCode
		result.Event.TotalPoints = payload.Data.PointGain.TotalPoints
	}
	return result, nil
}

func topicPrefix(topic string) string {
	for i := len(topic) - 1; i >= 0; i-- {
		if topic[i] == '.' {
			return topic[:i]
		}
	}
	return topic
}

func topicSuffix(topic string) string {
	for i := len(topic) - 1; i >= 0; i-- {
		if topic[i] == '.' {
			return topic[i+1:]
		}
	}
	return ""
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
