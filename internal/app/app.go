package app

import (
	"context"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"parasocial/internal/config"
	"parasocial/internal/tui"
)

// Run loads the application configuration and starts the terminal UI.
func Run(ctx context.Context) error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}

	program := tea.NewProgram(
		tui.New(cfg.Streamers),
		tea.WithContext(ctx),
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
	)
	_, err = program.Run()
	return err
}
