//go:build !windows

package core

import (
	"keep-mirrors/internal/env"
)

// readPersistedEnv reads the value from the user's profile, ignoring our own
// process environment.
func readPersistedEnv(name string) (string, error) {
	return env.Read(name)
}
