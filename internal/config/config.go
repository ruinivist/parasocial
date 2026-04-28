// config.go loads and validates the cwd config.toml file for the new CLI.
// It currently owns streamer list parsing, normalization, and the default
// config path behavior used by application startup.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

const configFileName = "config.toml"

// Config is the user-owned TOML configuration for parasocial.
type Config struct {
	Streamers []string `toml:"streamers"`
}

// DefaultPath returns the config file path relative to the current working directory.
func DefaultPath() string {
	return configFileName
}

// LoadDefault reads config from the current working directory.
func LoadDefault() (Config, error) {
	return Load(DefaultPath())
}

// Load reads, normalizes, and validates a TOML config file.
func Load(path string) (Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("config file not found at %s", path)
		}
		return Config{}, fmt.Errorf("load config %s: %w", path, err)
	}

	cfg.Streamers = normalizeStreamers(cfg.Streamers)
	if len(cfg.Streamers) == 0 {
		return Config{}, fmt.Errorf("config %s must define at least one streamer", path)
	}

	return cfg, nil
}

// normalizeStreamers deduplicates streamer names after applying input cleanup rules.
func normalizeStreamers(streamers []string) []string {
	seen := make(map[string]struct{}, len(streamers))
	normalized := make([]string, 0, len(streamers))

	for _, streamer := range streamers {
		streamer = normalizeStreamer(streamer)
		if streamer == "" {
			continue
		}
		if _, ok := seen[streamer]; ok {
			continue
		}
		seen[streamer] = struct{}{}
		normalized = append(normalized, streamer)
	}

	return normalized
}

// normalizeStreamer trims formatting noise and canonicalizes a single streamer name.
func normalizeStreamer(streamer string) string {
	streamer = strings.TrimSpace(streamer)
	streamer = strings.TrimPrefix(streamer, "#")
	return strings.ToLower(strings.TrimSpace(streamer))
}
