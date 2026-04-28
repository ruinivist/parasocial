package irc

import (
	"bufio"
	"context"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunOnceSendsAuthJoinAndRespondsToPing(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	lines := make(chan string, 10)
	serverErr := make(chan error, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					serverErr <- nil
					return
				}
				serverErr <- err
				return
			}
			line = strings.TrimRight(line, "\r\n")
			lines <- line
			switch line {
			case "NICK viewer":
				_, _ = conn.Write([]byte(":tmi.twitch.tv 001 viewer :Welcome\r\n"))
			case "JOIN #streamer":
				_, _ = conn.Write([]byte("PING :tmi.twitch.tv\r\n"))
			case "PONG :tmi.twitch.tv":
				_, _ = conn.Write([]byte(":viewer!viewer@viewer.tmi.twitch.tv JOIN #streamer\r\n"))
			case "QUIT :parasocial --once complete":
				serverErr <- nil
				return
			}
		}
	}()

	client := &Client{
		Addr:     listener.Addr().String(),
		Login:    "viewer",
		Token:    "token",
		Streamer: "streamer",
		Once:     true,
		Out:      io.Discard,
	}
	if err := client.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"PASS oauth:token",
		"NICK viewer",
		"JOIN #streamer",
		"PONG :tmi.twitch.tv",
		"QUIT :parasocial --once complete",
	}
	for _, expected := range want {
		select {
		case got := <-lines:
			if got != expected {
				t.Fatalf("line = %q, want %q", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q", expected)
		}
	}

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not finish")
	}
}

func TestRunEmitsJoinAndPostJoinEvents(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	events := make(chan Event, 4)
	serverErr := make(chan error, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				serverErr <- err
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch line {
			case "NICK viewer":
				_, _ = conn.Write([]byte(":tmi.twitch.tv 001 viewer :Welcome\r\n"))
			case "JOIN #streamer":
				_, _ = conn.Write([]byte(":viewer!viewer@viewer.tmi.twitch.tv JOIN #streamer\r\n"))
				_, _ = conn.Write([]byte(":someone!someone@someone.tmi.twitch.tv PRIVMSG #streamer :hello\r\n"))
				_, _ = conn.Write([]byte("PING :tmi.twitch.tv\r\n"))
			case "PONG :tmi.twitch.tv":
				serverErr <- nil
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		Addr:     listener.Addr().String(),
		Login:    "viewer",
		Token:    "token",
		Streamer: "streamer",
		Events: func(event Event) {
			events <- event
		},
		Out: io.Discard,
	}

	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()

	var got []Event
	for len(got) < 2 {
		select {
		case event := <-events:
			got = append(got, event)
			if event.Line == ":someone!someone@someone.tmi.twitch.tv PRIVMSG #streamer :hello" {
				cancel()
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for IRC events")
		}
	}

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil && err != io.EOF {
		t.Fatal(err)
	}

	want := []Event{
		{Streamer: "streamer", State: StateJoined, Line: "Joined #streamer as viewer"},
		{Streamer: "streamer", Line: ":someone!someone@someone.tmi.twitch.tv PRIVMSG #streamer :hello"},
	}
	if !reflect.DeepEqual(got[:2], want) {
		t.Fatalf("events = %#v, want %#v", got[:2], want)
	}
}

func TestAuthFailureReturnsError(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(":tmi.twitch.tv NOTICE * :Login authentication failed\r\n"))
	}()

	client := &Client{
		Addr:     listener.Addr().String(),
		Login:    "viewer",
		Token:    "bad-token",
		Streamer: "streamer",
		Out:      io.Discard,
	}
	err = client.Run(context.Background())
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if !strings.Contains(err.Error(), "authentication failure") {
		t.Fatalf("err = %v", err)
	}
}
