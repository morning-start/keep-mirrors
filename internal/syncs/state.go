package syncs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"keep-mirrors/internal/fsutil"
	"keep-mirrors/internal/paths"
	"keep-mirrors/internal/plugins"
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

// State records what the plugin directory currently holds, so an unchanged
// branch is a no-op.
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
	// FromSeed reports that SHA describes the snapshot compiled into the
	// binary rather than a previous sync. It matters because the two need
	// different wording: a fresh install is not "previously synced", but it is
	// also not out of date, and telling the user to sync would be noise.
	FromSeed bool `json:"from_seed,omitempty"`
	// Version is the plugin set version, recorded for diagnostics only.
	Version string `json:"version,omitempty"`
}

// statePath is where the sync state lives, next to the plugin directories.
func statePath() string {
	return filepath.Join(paths.AppDir(), "plugins-sync.json")
}

// LoadState reads the recorded state.
//
// A missing file is not an error and does not mean "nothing is installed": a
// fresh install runs the snapshot compiled into the binary. Returning that
// snapshot's provenance is what stops the first `km plugin sync` from
// re-downloading plugins the binary already carries.
func LoadState() (State, error) {
	var st State
	b, err := os.ReadFile(statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return stateFromSeed(), nil
		}
		return st, fmt.Errorf("read %s: %w", statePath(), err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		// A corrupt state file must not block syncing. Falling back to the
		// seed still lets an up-to-date install be a no-op, and a stale one do
		// a full download; either way the next successful run rewrites the file.
		return stateFromSeed(), nil
	}
	// A state file written by an older build may carry an empty SHA. Treat it
	// the same as "no state" so the seed still answers the freshness question.
	if st.SHA == "" {
		return stateFromSeed(), nil
	}
	return st, nil
}

// stateFromSeed describes what the binary already carries.
//
// This is the answer for an install that has never synced. Returning it rather
// than an empty state is what makes the first `km plugin sync` a no-op when the
// binary happens to ship the newest plugin set — which is the common case right
// after a release.
func stateFromSeed() State {
	p := plugins.Snapshot()
	if !p.Valid() {
		return State{}
	}
	return State{
		Repo:     DefaultRepo,
		Branch:   DefaultBranch,
		SHA:      p.SHA,
		FromSeed: true,
	}
}

// SaveState writes the state atomically (tmp → fsync → rename), so a crash
// mid-write cannot leave a torn file that would silently force a re-download
// on the next run.
func SaveState(st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := fsutil.WriteFile(statePath(), string(b), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", statePath(), err)
	}
	return nil
}
