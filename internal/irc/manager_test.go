package irc

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestManagerSyncStartsAtMostTwoTargets(t *testing.T) {
	t.Parallel()

	started := make(chan string, 3)
	manager := &Manager{
		RunClient: func(ctx context.Context, client *Client) error {
			started <- client.Streamer
			<-ctx.Done()
			return ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Sync(ctx, "viewer", "token", []Target{
		{Login: "alpha"},
		{Login: "beta"},
		{Login: "gamma"},
	})

	assertStartSet(t, started, "alpha", "beta")
	assertNoStart(t, started)
}

func TestManagerSyncKeepsExistingTargetsConnected(t *testing.T) {
	t.Parallel()

	started := make(chan string, 3)
	manager := &Manager{
		RunClient: func(ctx context.Context, client *Client) error {
			started <- client.Streamer
			<-ctx.Done()
			return ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Sync(ctx, "viewer", "token", []Target{{Login: "alpha"}})
	assertStartSet(t, started, "alpha")

	manager.Sync(ctx, "viewer", "token", []Target{{Login: "alpha"}})
	assertNoStart(t, started)
}

func TestManagerSyncCancelsRemovedTargets(t *testing.T) {
	t.Parallel()

	started := make(chan string, 4)
	canceled := make(chan string, 4)
	manager := &Manager{
		RunClient: func(ctx context.Context, client *Client) error {
			started <- client.Streamer
			<-ctx.Done()
			canceled <- client.Streamer
			return ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Sync(ctx, "viewer", "token", []Target{{Login: "alpha"}, {Login: "beta"}})
	assertStartSet(t, started, "alpha", "beta")

	manager.Sync(ctx, "viewer", "token", []Target{{Login: "beta"}, {Login: "gamma"}})
	assertStartSet(t, started, "gamma")
	assertCancels(t, canceled, "alpha")
	assertNoStart(t, started)
}

func TestManagerSyncRestartsTargetAfterFailure(t *testing.T) {
	t.Parallel()

	started := make(chan string, 2)
	var mu sync.Mutex
	attempts := map[string]int{}

	manager := &Manager{
		RunClient: func(ctx context.Context, client *Client) error {
			mu.Lock()
			attempts[client.Streamer]++
			attempt := attempts[client.Streamer]
			mu.Unlock()

			started <- client.Streamer
			if attempt == 1 {
				return errors.New("boom")
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Sync(ctx, "viewer", "token", []Target{{Login: "alpha"}})
	assertStartSet(t, started, "alpha")

	waitFor(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		_, ok := manager.active["alpha"]
		return !ok
	})

	manager.Sync(ctx, "viewer", "token", []Target{{Login: "alpha"}})
	assertStartSet(t, started, "alpha")
}

func TestManagerEmitsPendingAndDisconnectedEvents(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 4)
	manager := &Manager{
		Events: func(event Event) {
			events <- event
		},
		RunClient: func(ctx context.Context, client *Client) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	manager.Sync(ctx, "viewer", "token", []Target{{Login: "alpha"}})
	cancel()
	manager.Close()

	var got []Event
	for len(got) < 2 {
		select {
		case event := <-events:
			got = append(got, event)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for manager events")
		}
	}

	want := []Event{
		{Streamer: "alpha", State: StatePending},
		{Streamer: "alpha", State: StateDisconnected},
	}
	if !reflect.DeepEqual(got[:2], want) {
		t.Fatalf("events = %#v, want %#v", got[:2], want)
	}
}

func assertStartSet(t *testing.T, started <-chan string, want ...string) {
	t.Helper()

	got := make([]string, 0, len(want))
	for range want {
		select {
		case login := <-started:
			got = append(got, login)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %d starts", len(want))
		}
	}

	sort.Strings(got)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	for index := range sortedWant {
		if got[index] != sortedWant[index] {
			t.Fatalf("starts = %#v, want %#v", got, sortedWant)
		}
	}
}

func assertCancels(t *testing.T, canceled <-chan string, want ...string) {
	t.Helper()

	for _, expected := range want {
		select {
		case got := <-canceled:
			if got != expected {
				t.Fatalf("cancel = %q, want %q", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for cancel %q", expected)
		}
	}
}

func assertNoStart(t *testing.T, started <-chan string) {
	t.Helper()

	select {
	case got := <-started:
		t.Fatalf("unexpected start %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
