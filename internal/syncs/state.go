package syncs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"keep-mirrors/internal/paths"
)

// DefaultRepo is the project whose plugins branch holds the curated set.
const DefaultRepo = "morning-start/keep-mirrors"

// DefaultBranch is the branch that evolves independently of master.
const DefaultBranch = "plugins"

// PluginDirInRepo is where the plugin sources live inside the repository.
const PluginDirInRepo = "internal/plugins"

// Config selects what to sync.
type Config struct {
	// Repo is "owner/name".
	Repo string
	// Branch is the branch holding the plugins.
	Branch string
	// Dir is where downloaded plugins are written. Defaults to the official
	// plugin directory.
	Dir string
}

// DefaultConfig returns the configuration for the official plugin set.
func DefaultConfig() Config {
	return Config{Repo: DefaultRepo, Branch: DefaultBranch}
}

// State records what was last synced, so an unchanged branch is a no-op.
//
// The commit SHA is the whole point: comparing it is what lets the command
// answer "already up to date" without downloading anything, and it is the
// reason the download step can pin the SHA it just verified rather than
// re-resolving a branch name that may have moved in between.
type State struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// SHA is the commit the current plugin files came from.
	SHA string `json:"sha"`
	// Version is the plugin set version, recorded for diagnostics only.
	Version string `json:"version,omitempty"`
}

// statePath is where the sync state lives, next to the plugin directories.
func statePath() string {
	return filepath.Join(paths.AppDir(), "plugins-sync.json")
}

// LoadState reads the recorded state. A missing file means "never synced",
// which is not an error: a fresh install has only the embedded snapshot.
func LoadState() (State, error) {
	var st State
	b, err := os.ReadFile(statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, fmt.Errorf("read %s: %w", statePath(), err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		// A corrupt state file must not block syncing; treating it as "never
		// synced" simply makes the next run do a full download.
		return State{}, nil
	}
	return st, nil
}

// SaveState writes the state atomically enough for our purposes: a partial
// write would only cause an extra download next time, never data loss.
func SaveState(st State) error {
	if err := os.MkdirAll(paths.AppDir(), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", paths.AppDir(), err)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.WriteFile(statePath(), b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", statePath(), err)
	}
	return nil
}
