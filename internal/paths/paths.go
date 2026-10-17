// Package paths resolves the well-known locations keep-mirrors works with.
//
// Everything keep-mirrors writes is confined to the user's home directory or
// the per-user application data directory. There is deliberately no support
// for writing to system locations: a broken mirror should never be able to
// render a machine unbootable.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

const appName = "keep-mirrors"

var (
	home string
	cfg  string
)

// OverrideHome forces the home and config roots. It exists so tests can point
// the tool at a temporary directory; production code never calls it.
//
// The values are normally resolved once at init because they are read on
// every path expansion, but that caching made the whole program untestable.
// Resolving them on demand costs one os.UserHomeDir call, which the kernel
// answers from a cached environment block.
func OverrideHome(h, c string) {
	home, cfg = h, c
}

func resolve() {
	home, _ = os.UserHomeDir()
	if home == "" {
		home, _ = os.Getwd()
	}
	cfg, _ = os.UserConfigDir()
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
}

func init() { resolve() }

// Reset re-reads the home and config roots from the environment. Tests use it
// after changing HOME.
func Reset() { resolve() }

// Home returns the user's home directory.
func Home() string { return home }

// AppDir is the directory holding global settings, state and backups.
func AppDir() string { return filepath.Join(cfg, appName) }

// BackupDir holds point-in-time snapshots taken before each apply.
func BackupDir() string { return filepath.Join(AppDir(), "backups") }

// LogDir holds rotating logs.
func LogDir() string { return filepath.Join(AppDir(), "logs") }

// PluginRoot is the directory holding every plugin source km runs.
func PluginRoot() string { return filepath.Join(AppDir(), "plugins") }

// OfficialPluginDir holds the plugins curated by the project. km may rewrite
// this directory wholesale when it syncs from the registry, which is why user
// edits belong in UserPluginDir instead of here.
func OfficialPluginDir() string { return filepath.Join(PluginRoot(), "official") }

// UserPluginDir holds plugins the user brought themselves. km never writes
// here: it is the one place a local edit is guaranteed to survive a sync.
//
// The name is "user" rather than "local" on purpose. What distinguishes these
// plugins is who owns them, not where they came from, so a plugin fetched from
// a user-supplied URL later still belongs here.
func UserPluginDir() string { return filepath.Join(PluginRoot(), "user") }

// Expand replaces a leading "~" with the user's home directory and cleans the
// result. Relative paths are resolved against Home.
//
// Note that a bare drive-relative path such as "C:foo" is left alone rather
// than being joined to Home: on Windows it is a valid, distinct path and
// silently rewriting it would make containment checks lie.
func Expand(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	// filepath.IsAbs is false for "C:foo" on Windows, but joining that to
	// Home produces a bogus path. Treat anything with a volume name as
	// absolute.
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return filepath.Clean(p)
	}
	return filepath.Join(home, p)
}

// WithinHome reports whether p (after expansion) stays inside the home
// directory. It is the containment check used by the future sandbox and by
// the TUI's safety warnings today.
func WithinHome(p string) bool {
	abs := Expand(p)
	rel, err := filepath.Rel(home, abs)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// EnvOr returns os.Getenv(k) or def when unset.
func EnvOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
