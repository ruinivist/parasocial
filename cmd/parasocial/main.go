// main.go is the executable entrypoint for the rewritten CLI.
// It owns process-level concerns such as signal handling and exit codes.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"parasocial/internal/config"
	"parasocial/internal/tui"
)

// main sets up process cancellation and reports fatal startup errors to stderr.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load("config.toml")
	if err == nil {
		err = tui.Run(ctx, tui.Options{Streamers: cfg.Streamers})
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
