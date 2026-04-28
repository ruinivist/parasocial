package irc

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
)

// Target identifies one streamer channel that should have an active IRC connection.
type Target struct {
	Login string
}

// Manager reconciles the active IRC connections against the desired watched set.
type Manager struct {
	Addr        string
	Debug       bool
	Out         io.Writer
	Events      EventSink
	DialContext func(context.Context, string, string) (net.Conn, error)
	RunClient   func(context.Context, *Client) error

	mu     sync.Mutex
	active map[string]*managedClient
	wg     sync.WaitGroup
}

type managedClient struct {
	cancel context.CancelFunc
}

// Sync applies the desired IRC target set using the provided viewer credentials.
func (m *Manager) Sync(ctx context.Context, viewerLogin, accessToken string, targets []Target) {
	orderedTargets, desired := normalizeTargets(targets)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active == nil {
		m.active = make(map[string]*managedClient)
	}

	for login, client := range m.active {
		if _, ok := desired[login]; ok {
			continue
		}
		client.cancel()
		delete(m.active, login)
	}

	if viewerLogin == "" || accessToken == "" {
		return
	}

	for _, login := range orderedTargets {
		if _, ok := m.active[login]; ok {
			continue
		}

		runCtx, cancel := context.WithCancel(ctx)
		client := &managedClient{cancel: cancel}
		m.active[login] = client

		m.wg.Add(1)
		go func(streamer string, current *managedClient) {
			defer m.wg.Done()
			m.runClient(runCtx, viewerLogin, accessToken, streamer, current)
		}(login, client)
	}
}

func (m *Manager) runClient(ctx context.Context, viewerLogin, accessToken, streamer string, client *managedClient) {
	run := m.RunClient
	if run == nil {
		run = func(ctx context.Context, conn *Client) error {
			return conn.Run(ctx)
		}
	}

	m.emit(Event{Streamer: streamer, State: StatePending})

	_ = run(ctx, &Client{
		Addr:        m.addr(),
		Login:       viewerLogin,
		Token:       accessToken,
		Streamer:    streamer,
		Debug:       m.Debug,
		Out:         m.Out,
		Events:      m.Events,
		DialContext: m.DialContext,
	})
	m.emit(Event{Streamer: streamer, State: StateDisconnected})

	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.active[streamer]; ok && current == client {
		delete(m.active, streamer)
	}
}

// Close cancels every active connection and waits for their goroutines to exit.
func (m *Manager) Close() {
	m.mu.Lock()
	for login, client := range m.active {
		client.cancel()
		delete(m.active, login)
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) addr() string {
	if m.Addr != "" {
		return m.Addr
	}
	return DefaultAddr
}

func (m *Manager) emit(event Event) {
	if m.Events == nil {
		return
	}
	m.Events(event)
}

func normalizeTargets(targets []Target) ([]string, map[string]struct{}) {
	ordered := make([]string, 0, 2)
	normalized := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		login := strings.ToLower(strings.TrimSpace(target.Login))
		if login == "" {
			continue
		}
		if _, ok := normalized[login]; ok {
			continue
		}
		normalized[login] = struct{}{}
		ordered = append(ordered, login)
		if len(normalized) == 2 {
			break
		}
	}
	return ordered, normalized
}
