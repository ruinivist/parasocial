package miner

import "testing"

func TestParseFramePointsEarned(t *testing.T) {
	t.Parallel()

	frame, err := parseFrame([]byte(`{"type":"MESSAGE","data":{"topic":"community-points-user-v1.viewer","message":"{\"type\":\"points-earned\",\"data\":{\"timestamp\":\"2026-04-29T12:00:00Z\",\"balance\":{\"balance\":42,\"channel_id\":\"123\"}}}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Event.Topic != "community-points-user-v1" || frame.Event.MessageType != "points-earned" || frame.Event.ChannelID != "123" || frame.Event.Balance != 42 {
		t.Fatalf("event = %#v", frame.Event)
	}
}

func TestParseFramePointsEarnedPointGain(t *testing.T) {
	t.Parallel()

	frame, err := parseFrame([]byte(`{"type":"MESSAGE","data":{"topic":"community-points-user-v1.viewer","message":"{\"type\":\"points-earned\",\"data\":{\"timestamp\":\"2026-04-29T12:00:00Z\",\"balance\":{\"balance\":42,\"channel_id\":\"123\"},\"point_gain\":{\"reason_code\":\"WATCH_STREAK\",\"total_points\":450}}}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Event.ReasonCode != "WATCH_STREAK" || frame.Event.TotalPoints != 450 {
		t.Fatalf("event = %#v", frame.Event)
	}
}

func TestParseFrameClaimAvailable(t *testing.T) {
	t.Parallel()

	frame, err := parseFrame([]byte(`{"type":"MESSAGE","data":{"topic":"community-points-user-v1.viewer","message":"{\"type\":\"claim-available\",\"data\":{\"timestamp\":\"2026-04-29T12:00:00Z\",\"claim\":{\"id\":\"claim-1\",\"channel_id\":\"123\"}}}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Event.ClaimID != "claim-1" || frame.Event.ChannelID != "123" {
		t.Fatalf("event = %#v", frame.Event)
	}
}

func TestParseFrameVideoPlaybackFallsBackToTopicSuffix(t *testing.T) {
	t.Parallel()

	frame, err := parseFrame([]byte(`{"type":"MESSAGE","data":{"topic":"video-playback-by-id.321","message":"{\"type\":\"stream-down\",\"data\":{\"timestamp\":\"2026-04-29T12:00:00Z\"}}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Event.Topic != "video-playback-by-id" || frame.Event.ChannelID != "321" || frame.Event.MessageType != "stream-down" {
		t.Fatalf("event = %#v", frame.Event)
	}
}
