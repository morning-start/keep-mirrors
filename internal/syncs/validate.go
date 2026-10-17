package syncs

import (
	"fmt"
	"runtime"
	"sort"
	"strings"

	"keep-mirrors/internal/engine"
	"keep-mirrors/internal/paths"
	"keep-mirrors/internal/provider"
)

// validationCtx is the context plugins see while being validated.
//
// It describes the machine the sync is running on, which is the right thing to
// check against: a plugin that cannot plan on this platform would fail for this
// user regardless of what any other platform supports.
func validationCtx() provider.Context {
	return provider.Context{
		Platform: runtime.GOOS,
		Home:     paths.Home(),
		Values:   map[string]any{},
	}
}

// validatePlugin compiles a downloaded plugin in a sandbox and checks that it
// satisfies the contract this binary speaks, returning the plugin's own release
// version as declared by the module table.
//
// This is the guard the plugin branch relies on. A branch commit that raises
// api.major, or that calls a km.* function this binary never implemented, is
// rejected here instead of being written and failing later for every user.
//
// Compiling rather than pattern-matching matters: a Lua chunk only fails on an
// undefined global when it is actually called, and status()/plan() are called
// during validation, so a missing km.* surface shows up now.
//
// The version comes back from the same loaded module the checks ran against, so
// there is no second parse to drift from what was actually validated.
func validatePlugin(name string, body []byte) (string, error) {
	id := strings.TrimSuffix(name, ".lua")

	p, _, err := engine.Load(string(body), engine.DefaultOptions())
	if err != nil {
		return "", fmt.Errorf("does not compile against this binary's contract: %w", err)
	}
	defer p.Close()

	if p.ID() != id {
		return "", fmt.Errorf("file name says %q but the plugin declares id %q", id, p.ID())
	}
	if p.Meta().API.Major != engine.ContractMajor {
		return "", fmt.Errorf("targets contract %d.x, this binary speaks %d.x",
			p.Meta().API.Major, engine.ContractMajor)
	}
	// Run the two entry points the tool actually uses. A plugin whose status()
	// throws would otherwise look fine until the list view rendered.
	_ = p.Status(validationCtx())
	if _, err := p.Plan(validationCtx(), firstPresetOpts(p)); err != nil {
		return "", fmt.Errorf("plan() failed against this binary: %w", err)
	}
	return p.Meta().Version, nil
}

// setVersion summarises the versions a plugin set declares.
//
// It deliberately does not pick a "newest": plugins evolve independently, so a
// branch commit can legitimately bump one plugin and leave the rest, and the
// only honest answer is then that the set is mixed. An earlier version compared
// the strings with ">", which silently reported 1.9.0 as newer than 1.10.0.
//
// This value is diagnostics only — nothing gates on it.
func setVersion(versions map[string]bool) string {
	switch len(versions) {
	case 0:
		return ""
	case 1:
		for v := range versions {
			return v
		}
	}
	out := make([]string, 0, len(versions))
	for v := range versions {
		out = append(out, v)
	}
	sort.Strings(out)
	return strings.Join(out, ", ") + " (mixed)"
}

// firstPresetOpts returns the first preset's options, matching what the UI
// selects when the user has not touched the picker.
func firstPresetOpts(p *engine.LuaProvider) map[string]any {
	ps := p.Presets()
	if len(ps) == 0 {
		return map[string]any{}
	}
	return ps[0].Opts
}
