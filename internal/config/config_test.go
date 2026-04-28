package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadValidConfig(t *testing.T) {
	path := writeConfig(t, `streamers = [" Alpha ", "#Beta", "alpha", "", "  #Gamma  "]`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(cfg.Streamers, want) {
		t.Fatalf("Streamers = %#v, want %#v", cfg.Streamers, want)
	}
}

func TestLoadRejectsMissingStreamerList(t *testing.T) {
	path := writeConfig(t, `streamers = []`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "at least one streamer") {
		t.Fatalf("Load() error = %q, want empty streamer message", err)
	}
}

func TestLoadRejectsInvalidTOML(t *testing.T) {
	path := writeConfig(t, `streamers = [`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Fatalf("Load() error = %q, want load config context", err)
	}
}

func TestDefaultPathUsesCurrentWorkingDirectoryConfig(t *testing.T) {
	if got := DefaultPath(); got != "config.toml" {
		t.Fatalf("DefaultPath() = %q, want %q", got, "config.toml")
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "config file not found") {
		t.Fatalf("Load() error = %q, want missing file context", err)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
