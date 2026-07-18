package twitch

import "testing"

func BenchmarkBuildMinuteWatchedPayload(b *testing.B) {
	userID := "user123"
	channelID := "channel456"
	login := "streamer"
	metadata := &StreamMetadata{
		BroadcastID: "bc123",
		Game:        &Game{Name: "Science", ID: "123"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildMinuteWatchedPayload(userID, channelID, login, metadata)
	}
}

func BenchmarkEncodeMinuteWatchedPayload(b *testing.B) {
	payload := MinuteWatchedPayload{
		{
			Event: "minute_watched",
			Properties: minuteWatchedProperties{
				ChannelID:   "channel456",
				BroadcastID: "bc123",
				Player:      "site",
				UserID:      "user123",
			},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = payload.Encode()
	}
}
