// /cmd/pirated-hollow-knight/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"pirated-hollow-knight/internal/config"
	"pirated-hollow-knight/internal/installer"
	"pirated-hollow-knight/internal/launcher"
	"pirated-hollow-knight/internal/log"
)

func main() {
	// 1. Create a context that is cancelled on an interrupt signal.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// 2. Load all configuration from flags and arguments.
	cfg, err := config.Load()
	if err != nil {
		// Use a basic logger since the custom one isn't configured yet.
		fmt.Fprintf(os.Stderr, "[CRITICAL] Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// 3. Initialize the global logger with the level from the config.
	log.Init(cfg.LogLevel)

	// 4. Route to the appropriate command based on the loaded config.
	if cfg.RunClean {
		if err := launcher.RunClean(cfg); err != nil {
			log.Log.Fatal("Clean operation failed: %v", err)
		}
	} else {
		runDefault(ctx, cfg)
	}
}

// runDefault executes the main application logic: ensuring dependencies and launching the game.
func runDefault(ctx context.Context, cfg *config.Config) {
	log.Log.Info("--- Running Default Mode ---")

	if err := installer.EnsureDependencies(ctx, cfg); err != nil {
		log.Log.Fatal("Failed to satisfy dependencies: %v", err)
	}

	if err := launcher.LaunchGame(ctx, cfg); err != nil {
		log.Log.Fatal("Game launch failed: %v", err)
	}

	log.Log.Info("--- Script finished ---")
}
