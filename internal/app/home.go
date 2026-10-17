package app

import "keep-mirrors/internal/paths"

// userHomeDir resolves the home directory through paths so a test that
// redirects HOME is honoured. The indirection exists so tests can stub it
// without touching the filesystem.
func userHomeDir() (string, error) { return paths.Home(), nil }
