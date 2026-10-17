//go:build windows

package core

import (
	"keep-mirrors/internal/env"
)

// readPersistedEnv reads the value from the registry store, ignoring our own
// process environment. On Windows this is the authoritative source for what
// future processes will see.
func readPersistedEnv(name string) (string, error) {
	return env.Read(name)
}
