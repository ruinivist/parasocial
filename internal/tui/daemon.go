package tui

import (
	"context"
	"fmt"
	"strings"
)

// RunDaemon starts the daemon mode which just logs everything instead of UI.
func RunDaemon(ctx context.Context, options Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	rt := newRuntime(ctx, options)
	state := options.AuthState
	if state == nil {
		var err error
		state, err = rt.reuseAuth()
		if err != nil {
			return err
		}
	}

	if state == nil {
		fmt.Println("Starting authentication...")
		authChan := make(chan AuthUpdate)
		rt.startAuth(authChan)

		var authErr error
		for authMsg := range authChan {
			if authMsg.Line != "" {
				fmt.Println(authMsg.Line)
			}
			if authMsg.Done {
				if authMsg.Err != nil {
					authErr = authMsg.Err
				} else if authMsg.State != nil {
					state = authMsg.State
				}
				break
			}
		}

		if authErr != nil {
			return fmt.Errorf("authentication failed: %w", authErr)
		}
		fmt.Println("Authentication complete.")
	}

	fmt.Println("Starting streamer resolution...")
	streamerChan := make(chan StreamerUpdate)
	rt.startResolve(state, streamerChan)

	for streamerMsg := range streamerChan {
		if streamerMsg.Done {
			break
		}
		if streamerMsg.Err != nil {
			fmt.Printf("Error: %v\n", streamerMsg.Err)
		}
		if streamerMsg.IRC != nil {
			if message, ok := formatIRCChatLine(streamerMsg.IRC.Line); ok {
				fmt.Printf("[IRC] %s: %s\n", streamerMsg.IRC.Login, message)
			}
		}
		if streamerMsg.Miner != nil {
			line := strings.TrimSpace(streamerMsg.Miner.Line)
			if line != "" {
				fmt.Printf("[Miner] %s: %s\n", streamerMsg.Miner.Login, line)
			}
		}
	}

	return nil
}
