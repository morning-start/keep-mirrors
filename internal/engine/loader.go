// Package engine's plugin loader.
//
// Plugins are looked up in two places, in order:
//
//	<user config dir>/keep-mirrors/plugins/   (user, wins on name collision)
//	plugins/                                 (shipped with the binary)
//
// The split is what makes an override possible without forking: dropping a
// copy of pip.lua in the user directory and editing it replaces the built-in
// behaviour at the next start.
package engine

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"keep-mirrors/internal/paths"
)

// BuiltinFS holds the plugins compiled into the binary. It is set by the
// main package from its own embed directive, because Go only allows a package
// to embed files beneath its own directory.
var BuiltinFS embed.FS

// BuiltinDir is the directory inside BuiltinFS that holds the plugins.
const BuiltinDir = "plugins"

// Loader finds and loads plugins.
type Loader struct {
	// UserDir overrides the user plugin directory; empty means the default.
	UserDir string
	// Options is applied to every plugin state.
	Options Options
	// Builtin is the source of built-in plugins.
	Builtin embed.FS
}

// NewLoader returns a loader with the default options.
func NewLoader() *Loader {
	return &Loader{Options: DefaultOptions(), Builtin: BuiltinFS}
}

// UserPluginDir is where user plugins live.
func (l *Loader) UserPluginDir() string {
	if l.UserDir != "" {
		return l.UserDir
	}
	return filepath.Join(paths.AppDir(), "plugins")
}

// PluginSource describes where a plugin came from, for diagnostics.
type PluginSource struct {
	ID   string
	Path string
	// Builtin reports whether the plugin shipped with the binary.
	Builtin bool
	// Err is non-nil when the plugin failed to load.
	Err error
}

// List returns the plugins that would be loaded, without loading them. It is
// what `km plugin list` shows, and it reports broken plugins instead of
// hiding them.
func (l *Loader) List() []PluginSource {
	byID := map[string]PluginSource{}

	// Built-ins first, so user plugins can be detected as overrides.
	if l.Builtin != (embed.FS{}) {
		entries, err := l.Builtin.ReadDir(BuiltinDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
					continue
				}
				id := strings.TrimSuffix(e.Name(), ".lua")
				byID[id] = PluginSource{
					ID: id, Path: "builtin:" + BuiltinDir + "/" + e.Name(), Builtin: true,
				}
			}
		}
	}

	if userDir := l.UserPluginDir(); userDir != "" {
		entries, err := os.ReadDir(userDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
					continue
				}
				id := strings.TrimSuffix(e.Name(), ".lua")
				byID[id] = PluginSource{
					ID: id, Path: filepath.Join(userDir, e.Name()),
				}
			}
		}
	}

	out := make([]PluginSource, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
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

// readSource returns the plugin source from the filesystem or the embedded FS.
func (l *Loader) readSource(src PluginSource) (string, error) {
	if src.Builtin {
		b, err := l.Builtin.ReadFile(src.Path[len("builtin:"):])
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := os.ReadFile(src.Path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
