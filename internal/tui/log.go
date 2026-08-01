package tui

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// Logging in the TUI must never touch stdout or stderr: both belong to the
// display, and a stray write corrupts the frame. Everything goes to a file
// under $XDG_STATE_HOME, which is also what makes a crashed session
// diagnosable after the terminal has been restored.

// OpenLog opens the TUI log file, creating its directory as needed.
//
// The returned closer must be called on exit. If the file cannot be opened,
// logging is directed to io.Discard rather than failing: an unwritable log
// directory is not a reason to refuse to run.
func OpenLog(path string) (*slog.Logger, io.Closer, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return discardLogger(), nopCloser{}, fmt.Errorf("create log directory: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return discardLogger(), nopCloser{}, fmt.Errorf("open %s: %w", path, err)
	}

	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return logger, f, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
