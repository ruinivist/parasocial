// main.go is the executable entrypoint for the rewritten CLI.
// It owns process-level concerns such as signal handling, exit codes,
// and delegating the actual application startup to internal/app.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"parasocial/internal/app"
)

// main sets up process cancellation and reports fatal startup errors to stderr.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
