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
// directory. This is the purely lexical check: it is cheap, has no I/O, and is
// what the TUI's safety warnings use.
//
// It deliberately does not resolve symlinks, so it is NOT sufficient on its own
// as a security boundary. Use WithinHomeResolved for that.
func WithinHome(p string) bool {
	return within(home, Expand(p))
}

// WithinHomeResolved is WithinHome after resolving symlinks, for every caller
// that is actually going to open the file.
//
// The lexical check alone is not a boundary: on POSIX a symlink inside home
// pointing at /etc, read through km.fs.read, passes WithinHome (the path really
// does start with the home directory) and then the open follows the link out.
// Resolving both the candidate and home closes that, and resolving home too
// matters because home is frequently itself a symlink (on macOS /home, and any
// Windows path reached through an 8.3 short name).
//
// A path that does not exist yet is resolved as far as it does exist, so the
// check still works for "would this write land inside home".
func WithinHomeResolved(p string) bool {
	abs := Expand(p)
	if !within(home, abs) {
		return false
	}
	// Resolve home the same way as the candidate. EvalSymlinks alone fails
	// when home does not exist — a freshly implemented profile, a temp dir
	// some other test already deleted — and then root stays in its unresolved
	// form while the candidate is resolved through its nearest existing
	// ancestor. The two then disagree even though the candidate is plainly
	// inside, which is how a Windows 8.3 home (RUNNER~1) turned every
	// km.fs.read into "outside the home directory".
	root := home
	if r, ok := resolveExisting(home); ok {
		root = r
	}
	resolved, ok := resolveExisting(abs)
	if !ok {
		return false
	}
	return within(root, resolved)
}

// within reports whether abs is root or sits below it. Both must be clean
// absolute paths.
func within(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// resolveExisting resolves the symlinks in p as far as p exists, then re-appends
// the components that do not. EvalSymlinks fails outright on a missing path,
// which would otherwise make every "does this not-yet-created file stay in
// home" question answerable only as "no".
func resolveExisting(p string) (string, bool) {
	cur := p
	var tail []string
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Walked to the volume root without resolving anything.
			return "", false
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}
