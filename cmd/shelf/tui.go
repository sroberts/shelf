package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sroberts/shelf/internal/tui"
)

// runTUI starts the terminal interface.
//
// Everything the TUI can do is also reachable headless; it is a frontend on the
// same internal API, not a place where logic lives. That is what makes
// --no-tui, cron jobs, and piping to jq all keep working.
func runTUI(ctx context.Context, a *app) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}
	db, err := a.index()
	if err != nil {
		return err
	}

	// Logs go to a file, never to stdout or stderr: those belong to the
	// renderer, and a stray write corrupts the frame.
	logger, closer, logErr := tui.OpenLog(cfg.Paths.LogFile())
	defer closer.Close()
	if logErr != nil {
		// An unwritable log directory is not a reason to refuse to run.
		fmt.Fprintf(os.Stderr, "warning: %v\n", logErr)
	}
	logger.Info("starting tui",
		"library", cfg.LibraryRoot,
		"devices", len(cfg.Devices),
		"graphics", tui.DetectGraphics(cfg.UI.Graphics))

	model := tui.New(ctx, &tui.App{Config: cfg, DB: db, Log: logger})

	program := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := program.Run(); err != nil {
		logger.Error("tui exited with an error", "err", err)
		return err
	}
	logger.Info("tui exited cleanly")
	return nil
}

// isInteractive reports whether stdout is a terminal.
//
// Launching a full-screen UI into a pipe produces escape-code soup, so a
// non-interactive stdout falls back to help text.
func isInteractive() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
