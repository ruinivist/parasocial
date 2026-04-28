package twitch

import (
	"context"
	"encoding/json"
	"errors"
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
