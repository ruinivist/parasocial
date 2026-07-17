// store.go defines the persisted auth bundle stored as cookies.json in the cwd.
// It handles loading, saving, and validating the auth state so cached login data
// can be reused across runs without redoing the Twitch device flow every time.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const authFileName = "cookies.json"

// State is the persisted cwd auth bundle for the app.
type State struct {
	AccessToken string `json:"access_token"`
	Login       string `json:"login"`
	DeviceID    string `json:"device_id"`
}

// DefaultPath returns the cwd auth bundle path.
func DefaultPath() string {
	return authFileName
}

// LoadState reads a persisted auth bundle from disk when one exists.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if err := state.validate(); err != nil {
		return nil, fmt.Errorf("validate auth state %s: %w", path, err)
	}
	return &state, nil
}

// SaveState writes the validated auth bundle to the configured cwd path.
func SaveState(path string, state *State) error {
	if state == nil {
		return fmt.Errorf("auth state is nil")
	}
	if err := state.validate(); err != nil {
		return fmt.Errorf("validate auth state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// validate rejects partial auth bundles so loading and saving use one canonical shape.
func (s *State) validate() error {
	switch {
	case s.AccessToken == "":
		return fmt.Errorf("missing access_token")
	case s.Login == "":
		return fmt.Errorf("missing login")
	case s.DeviceID == "":
		return fmt.Errorf("missing device_id")
	default:
		return nil
	}
}
