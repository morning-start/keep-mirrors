// Package engine's plugin loader.
//
// Plugins live on disk, in two directories that are scanned in a fixed order:
//
//	<appdata>/keep-mirrors/plugins/user/       user-supplied, wins on id clash
//	<appdata>/keep-mirrors/plugins/official/   curated, rewritten by sync
//
// Nothing is compiled into the binary as a runtime source. The embedded copy
// in internal/plugins is only a seed: on first run it is unpacked into
// official/ so the tool works offline, and from then on the directory is the
// single source of truth. That is what makes hot reload and registry sync
// possible without rebuilding the binary.
package engine

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"keep-mirrors/internal/paths"
)

// Origin says where a plugin came from, which decides whether km may overwrite
// it and which directory a user should edit.
type Origin string

const (
	// OriginOfficial is the curated set, synced from the registry.
	OriginOfficial Origin = "official"
	// OriginUser is user-supplied. km never writes here.
	OriginUser Origin = "user"
)

// SeedFS holds the official plugins compiled into the binary. It is a seed,
// not a runtime source: main sets it from internal/plugins, and the loader only
// reads it when official/ holds no plugins yet.
//
// It is a package variable rather than a loader field so that every entry
// point — including tests that never call main — gets the same behaviour
// without threading the FS through each call site.
var SeedFS fs.FS

// SetSeed installs the seed snapshot. main calls it once; tests that exercise
// seeding call it directly.
func SetSeed(fsys fs.FS) { SeedFS = fsys }

// SeedDir is the directory inside SeedFS holding the seed plugins. The seed
// package embeds *.lua at its root, so this is ".".
const SeedDir = "."

// Loader finds and loads plugins.
type Loader struct {
	// OfficialDir overrides the official plugin directory.
	OfficialDir string
	// UserDir overrides the user plugin directory.
	UserDir string
	// Options is applied to every plugin state.
	Options Options
	// Seed is the source of the first-run plugin snapshot.
	Seed fs.FS
}

// NewLoader returns a loader with the default options.
func NewLoader() *Loader {
	return &Loader{Options: DefaultOptions(), Seed: SeedFS}
}

// OfficialPluginDir is where curated plugins live.
func (l *Loader) OfficialPluginDir() string {
	if l.OfficialDir != "" {
		return l.OfficialDir
	}
	return paths.OfficialPluginDir()
}

// UserPluginDir is where user plugins live.
func (l *Loader) UserPluginDir() string {
	if l.UserDir != "" {
		return l.UserDir
	}
	return paths.UserPluginDir()
}

// PluginSource describes where a plugin came from, for diagnostics.
type PluginSource struct {
	ID   string
	Path string
	// Origin distinguishes curated plugins from user-supplied ones.
	Origin Origin
	// Err is non-nil when the plugin failed to load.
	Err error
}

// List returns the plugins that would be loaded, without loading them. It is
// what `km plugin list` shows, and it reports broken plugins instead of
// hiding them.
//
// Official is scanned first so a user plugin with the same id replaces it,
// which is how a user pins a local fix without patching the official copy.
func (l *Loader) List() []PluginSource {
	byID := map[string]PluginSource{}

	for _, dir := range []struct {
		path   string
		origin Origin
	}{
		{l.OfficialPluginDir(), OriginOfficial},
		{l.UserPluginDir(), OriginUser},
	} {
		for _, src := range listDir(dir.path, dir.origin) {
			byID[src.ID] = src
		}
	}

	out := make([]PluginSource, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// listDir enumerates the .lua files in one directory. A missing directory is
// not an error: the official one may not be seeded yet and the user one may
// never be created.
func listDir(dir string, origin Origin) []PluginSource {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []PluginSource
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
			continue
		}
		out = append(out, PluginSource{
			ID:     strings.TrimSuffix(e.Name(), ".lua"),
			Path:   filepath.Join(dir, e.Name()),
			Origin: origin,
		})
	}
	return out
}

// Load reads and compiles every plugin. Plugins that fail to load are
// skipped and reported in the returned slice, so one broken plugin does not
// take the whole tool down.
func (l *Loader) Load() ([]*LuaProvider, []PluginSource, error) {
	var (
		provs []*LuaProvider
		diag  []PluginSource
	)

	for _, src := range l.List() {
		body, err := l.readSource(src)
		if err != nil {
			src.Err = err
			diag = append(diag, src)
			continue
		}
		p, _, err := Load(body, l.Options)
		if err != nil {
			src.Err = err
			diag = append(diag, src)
			continue
		}
		provs = append(provs, p)
	}
	return provs, diag, nil
}

// LoadOne compiles a single plugin by id. It is used by `km plugin show`.
func (l *Loader) LoadOne(id string) (*LuaProvider, *State, error) {
	for _, src := range l.List() {
		if src.ID != id {
			continue
		}
		body, err := l.readSource(src)
		if err != nil {
			return nil, nil, err
		}
		return Load(body, l.Options)
	}
	return nil, nil, fmt.Errorf("no plugin named %q", id)
}

// readSource returns the plugin source from disk.
//
// Reading from the filesystem is the whole point: the file is the runtime
// source, so editing it (or syncing a new version into it) takes effect
// without rebuilding.
func (l *Loader) readSource(src PluginSource) (string, error) {
	b, err := os.ReadFile(src.Path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// SeedIfEmpty writes the embedded plugin snapshot into the official directory,
// but only when that directory holds no plugins yet.
//
// The one-shot guard matters: seeding on every start would silently revert
// whatever a registry sync had installed, and would also resurrect plugins the
// user deliberately deleted.
func (l *Loader) SeedIfEmpty() (int, error) {
	if l.Seed == nil {
		return 0, nil
	}
	dir := l.OfficialPluginDir()
	if len(listDir(dir, OriginOfficial)) > 0 {
		return 0, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("create %s: %w", dir, err)
	}

	entries, err := fs.ReadDir(l.Seed, SeedDir)
	if err != nil {
		return 0, fmt.Errorf("read seed: %w", err)
	}
	written := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
			continue
		}
		body, err := fs.ReadFile(l.Seed, seedPath(e.Name()))
		if err != nil {
			return written, fmt.Errorf("read seed %s: %w", e.Name(), err)
		}
		dst := filepath.Join(dir, e.Name())
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return written, fmt.Errorf("seed %s: %w", dst, err)
		}
		written++
	}
	return written, nil
}

// seedPath joins a name onto SeedDir, tolerating SeedDir being "." so the
// embedded files sit at the root of the FS.
func seedPath(name string) string {
	if SeedDir == "." || SeedDir == "" {
		return name
	}
	return SeedDir + "/" + name
}
