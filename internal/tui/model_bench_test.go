package tui

import "testing"
import "parasocial/internal/twitch"

func BenchmarkFormatIRCChatLine(b *testing.B) {
	line := ":jules!jules@jules.tmi.twitch.tv PRIVMSG #parasocial :Hello world this is a test message"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = formatIRCChatLine(line)
	}
}

func BenchmarkNormalizeKey(b *testing.B) {
	key := "SomeStreamerName"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = normalizeKey(key)
	}
}

func BenchmarkIsActive(b *testing.B) {
	entry := twitch.StreamerEntry{Status: twitch.StreamerReady}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isActive(entry)
	}
}
