package tui

import (
	"path/filepath"

	"keep-mirrors/internal/paths"
)

// homeDir resolves the user's home directory for display shortening. It goes
// through paths so a redirected HOME is honoured.
func homeDir() string {
	return filepath.Clean(paths.Home())
}
