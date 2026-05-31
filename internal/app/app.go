// app.go owns process-level application bootstrap before handing control to the TUI.
package app

import (
	"context"

	"parasocial/internal/config"
	"parasocial/internal/tui"
)

// Run loads configuration and starts the terminal UI.
func Run(ctx context.Context, daemon bool) error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	if daemon {
		return tui.RunDaemon(ctx, tui.Options{Streamers: cfg.Streamers})
	}
	return tui.Run(ctx, tui.Options{Streamers: cfg.Streamers})
}
