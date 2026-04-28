// app.go wires together config loading, auth bootstrap, and the Bubble Tea program.
// It decides whether cached Twitch auth can be reused and, when needed,
// connects the interactive login flow to the TUI through auth update messages.
package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"parasocial/internal/auth"
	"parasocial/internal/config"
	"parasocial/internal/tui"
)

// Run loads the application configuration and starts the terminal UI.
func Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	authClient := auth.NewClient(httpClient)
	authPath := auth.DefaultPath()

	state, err := authClient.ReuseAuth(ctx, authPath)
	if err != nil {
		return err
	}

	program := tea.NewProgram(
		tui.New(tui.Options{
			Streamers: cfg.Streamers,
			AuthState: state,
			StartAuth: func(ch chan<- tui.AuthUpdate) {
				go func() {
					defer close(ch)

					state, err := authClient.EnsureAuth(ctx, authPath, func(line string) {
						ch <- tui.AuthUpdate{Line: strings.TrimRight(line, "\n")}
					})
					if err != nil {
						ch <- tui.AuthUpdate{
							Line: fmt.Sprintf("Authentication failed: %v", err),
							Err:  err,
							Done: true,
						}
						return
					}

					ch <- tui.AuthUpdate{State: state, Done: true}
				}()
			},
		}),
		tea.WithContext(ctx),
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
	)
	_, err = program.Run()
	return err
}
