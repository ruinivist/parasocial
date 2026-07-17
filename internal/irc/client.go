// Package irc manages Twitch IRC chat connections for watched streamers.
package irc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const DefaultAddr = "irc.chat.twitch.tv:6667"

// ConnectionState describes the lifecycle state of one IRC connection.
type ConnectionState string

const (
	StatePending      ConnectionState = "pending"
	StateJoined       ConnectionState = "joined"
	StateDisconnected ConnectionState = "disconnected"
)

// Event carries one IRC lifecycle change or log line for a streamer connection.
type Event struct {
	Streamer string
	State    ConnectionState
	Line     string
}

// EventSink consumes IRC events emitted by a client or manager.
type EventSink func(Event)

// Client maintains one Twitch IRC connection for a single streamer channel.
type Client struct {
	Addr        string
	Login       string
	Token       string
	Streamer    string
	Once        bool
	Events      EventSink
	DialContext func(context.Context, string, string) (net.Conn, error)
}

// Run connects, authenticates, joins the configured streamer channel, and stays connected.
func (c *Client) Run(ctx context.Context) (err error) {
	if c.Login == "" {
		return errors.New("missing Twitch login")
	}
	if c.Token == "" {
		return errors.New("missing OAuth access token")
	}
	if c.Streamer == "" {
		return errors.New("missing streamer")
	}
	defer func() {
		if ctx.Err() != nil {
			err = nil
		}
	}()

	addr := c.Addr
	if addr == "" {
		addr = DefaultAddr
	}

	conn, err := c.dial(ctx, addr)
	if err != nil {
		return fmt.Errorf("connect to Twitch IRC: %w", err)
	}

	session := &session{conn: conn}
	defer session.close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.send("QUIT :parasocial shutting down")
			session.close()
		case <-done:
		}
	}()

	if err := session.send("PASS oauth:" + c.Token); err != nil {
		return err
	}
	if err := session.send("NICK " + c.Login); err != nil {
		return err
	}
	if err := session.send("JOIN #" + c.Streamer); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	joined := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if joined {
				return fmt.Errorf("network interruption after join: %w", err)
			}
			return fmt.Errorf("IRC connection closed before join confirmation: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")
		if payload, ok := pingPayload(line); ok {
			if err := session.send("PONG " + payload); err != nil {
				return err
			}
			continue
		}

		if isAuthFailure(line) {
			return fmt.Errorf("IRC authentication failure: %s", line)
		}
		if isJoinDenied(line, c.Streamer) {
			return fmt.Errorf("IRC join denied: %s", line)
		}
		if isWelcome(line) {
			continue
		}
		if isJoinConfirmation(line, c.Login, c.Streamer) {
			joinedLine := fmt.Sprintf("Joined #%s as %s", c.Streamer, c.Login)
			c.emit(StateJoined, joinedLine)
			joined = true
			if c.Once {
				if err := session.send("QUIT :parasocial --once complete"); err != nil {
					return err
				}
				return nil
			}
			continue
		}
		if joined {
			c.emit("", line)
		}
	}
}

type session struct {
	conn net.Conn
	mu   sync.Mutex
	once sync.Once
}

func (s *session) send(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := fmt.Fprintf(s.conn, "%s\r\n", line)
	return err
}

func (s *session) close() {
	s.once.Do(func() {
		_ = s.conn.SetDeadline(time.Now())
		_ = s.conn.Close()
	})
}

func (c *Client) dial(ctx context.Context, addr string) (net.Conn, error) {
	if c.DialContext != nil {
		return c.DialContext(ctx, "tcp", addr)
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	return dialer.DialContext(ctx, "tcp", addr)
}

func (c *Client) emit(state ConnectionState, line string) {
	if c.Events == nil {
		return
	}
	c.Events(Event{
		Streamer: c.Streamer,
		State:    state,
		Line:     line,
	})
}

func pingPayload(line string) (string, bool) {
	if strings.HasPrefix(line, "PING ") {
		return strings.TrimSpace(strings.TrimPrefix(line, "PING ")), true
	}
	return "", false
}

func isWelcome(line string) bool {
	return strings.Contains(line, " 001 ")
}

func isAuthFailure(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "login authentication failed") ||
		strings.Contains(lower, "improperly formatted auth") ||
		strings.Contains(lower, "error logging in")
}

func isJoinDenied(line, streamer string) bool {
	lower := strings.ToLower(line)
	channel := "#" + strings.ToLower(streamer)
	return (strings.Contains(lower, " 403 ") && strings.Contains(lower, channel)) ||
		(strings.Contains(lower, "notice "+channel) && strings.Contains(lower, "banned")) ||
		(strings.Contains(lower, "notice "+channel) && strings.Contains(lower, "not permitted"))
}

func isJoinConfirmation(line, login, streamer string) bool {
	lower := strings.ToLower(line)
	channel := "#" + strings.ToLower(streamer)
	return strings.Contains(lower, " join "+channel) &&
		(strings.HasPrefix(lower, ":"+strings.ToLower(login)+"!") || strings.Contains(lower, "!"))
}
