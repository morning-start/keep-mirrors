package engine

import (
	"fmt"
	"strings"

	"keep-mirrors/internal/config"
	"keep-mirrors/internal/core"
	"keep-mirrors/internal/marker"
	"keep-mirrors/internal/provider"

	"github.com/yuin/gopher-lua"
)

// markerFormat aliases the marker package's syntax values under shorter names,
// so the Lua-facing switch below reads cleanly.
type markerFormat = marker.Format

const (
	markerFormatHash      = marker.Hash
	markerFormatSemicolon = marker.Semicolon
	markerFormatSlash     = marker.Slash
	markerFormatXML       = marker.XML
)

// LuaProvider adapts a Lua plugin module to the Go Provider interface.
//
// The mapping is mechanical: a Lua table field becomes a Go struct field, and
// a Lua function becomes a Go method that marshals its arguments, calls it,
// and unmarshals the result. Keeping this translation in one place means the
// Lua contract can evolve without touching the TUI or the core engine.
type LuaProvider struct {
	state *State
	mod   *lua.LTable // the module table returned by the plugin

	id       string
	title    string
	category provider.Category
	meta     provider.Meta

	presets  []provider.Preset
	settings []provider.Setting
}

// Load compiles src in a fresh sandboxed state and reads the module table.
func Load(src string, opts Options) (*LuaProvider, *State, error) {
	st, err := New(opts)
	if err != nil {
		return nil, nil, err
	}
	if err := st.LoadChunk(src); err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("compile plugin: %w", err)
	}

	mod := st.Module()
	if mod == nil {
		st.Close()
		return nil, nil, fmt.Errorf("plugin did not return a module table")
	}

	p := &LuaProvider{state: st, mod: mod}
	if err := p.readMeta(mod); err != nil {
		st.Close()
		return nil, nil, err
	}
	p.presets = readPresets(p.mod)
	p.settings = readSettings(p.mod)
	return p, st, nil
}

// readMeta pulls the identifying fields and validates the contract version.
func (p *LuaProvider) readMeta(tbl *lua.LTable) error {
	p.id = strOf(tbl, "id")
	p.title = strOf(tbl, "title")
	cat := strOf(tbl, "category")

	if p.id == "" {
		return fmt.Errorf("plugin is missing id")
	}
	if p.title == "" {
		p.title = p.id
	}
	if cat == "" {
		cat = string(provider.CategoryTool)
	}
	p.category = provider.Category(cat)

	p.meta = provider.Meta{
		Version:     strOf(tbl, "version"),
		Author:      strOf(tbl, "author"),
		License:     strOf(tbl, "license"),
		Homepage:    strOf(tbl, "homepage"),
		Description: strOf(tbl, "description"),
		Platforms:   readStrings(tbl, "platforms"),
		Detects:     readStrings(tbl, "detects"),
		Envs:        readStrings(tbl, "envs"),
	}
	if api, ok := tbl.RawGetString("api").(*lua.LTable); ok {
		p.meta.API = provider.ContractVersion{
			Major: int(lua.LVAsNumber(api.RawGetString("major"))),
			Minor: int(lua.LVAsNumber(api.RawGetString("minor"))),
		}
	}
	if p.meta.API.Major == 0 {
		p.meta.API.Major = 1
	}
	if p.meta.API.Major != ContractMajor {
		return fmt.Errorf("plugin %q targets contract %d.x, this binary speaks %d.x",
			p.id, p.meta.API.Major, ContractMajor)
	}
	return nil
}

// ContractMajor is the plugin contract version this binary implements.
const ContractMajor = 1

func readPresets(tbl *lua.LTable) []provider.Preset {
	ps, ok := tbl.RawGetString("presets").(*lua.LTable)
	if !ok {
		return nil
	}
	var out []provider.Preset
	ps.ForEach(func(_, v lua.LValue) {
		pt, ok := v.(*lua.LTable)
		if !ok {
			return
		}
		out = append(out, provider.Preset{
			ID:    strOf(pt, "id"),
			Label: strOf(pt, "label"),
			Note:  strOf(pt, "note"),
			Opts:  tableToMap(pt.RawGetString("opts")),
		})
	})
	// Declaration order is preserved on purpose: the first preset is the one
	// the UI preselects, so sorting by id would silently change which mirror
	// a user gets when they never touch the picker.
	return out
}

func readSettings(tbl *lua.LTable) []provider.Setting {
	ss, ok := tbl.RawGetString("settings").(*lua.LTable)
	if !ok {
		return nil
	}
	var out []provider.Setting
	ss.ForEach(func(_, v lua.LValue) {
		st, ok := v.(*lua.LTable)
		if !ok {
			return
		}
		s := provider.Setting{
			Key:      strOf(st, "key"),
			Type:     strOf(st, "type"),
			Label:    strOf(st, "label"),
			Validate: strOf(st, "validate"),
			Help:     strOf(st, "help"),
		}
		if d := st.RawGetString("default"); d != lua.LNil {
			s.Default = luaToGo(d)
		}
		if c, ok := st.RawGetString("choices").(*lua.LTable); ok {
			for i := 1; i <= c.Len(); i++ {
				s.Choices = append(s.Choices, lua.LVAsString(c.RawGetInt(i)))
			}
		}
		if s.Type == "" {
			s.Type = "string"
		}
		out = append(out, s)
	})
	return out
}

func readStrings(tbl *lua.LTable, key string) []string {
	t, ok := tbl.RawGetString(key).(*lua.LTable)
	if !ok {
		return nil
	}
	var out []string
	for i := 1; i <= t.Len(); i++ {
		out = append(out, lua.LVAsString(t.RawGetInt(i)))
	}
	return out
}

// ── provider.Provider implementation ────────────────────────────────────

func (p *LuaProvider) ID() string                   { return p.id }
func (p *LuaProvider) Title() string                { return p.title }
func (p *LuaProvider) Category() provider.Category  { return p.category }
func (p *LuaProvider) Meta() provider.Meta          { return p.meta }
func (p *LuaProvider) Presets() []provider.Preset   { return p.presets }
func (p *LuaProvider) Settings() []provider.Setting { return p.settings }

// Status calls the plugin's status() and maps the returned table.
func (p *LuaProvider) Status(ctx provider.Context) provider.Status {
	res, err := p.state.CallMethod(p.mod, "status", ctxTable(p.state.L, ctx))
	if err != nil {
		return provider.Status{Note: "status failed: " + err.Error()}
	}
	if len(res) == 0 {
		return provider.Status{}
	}
	t, ok := res[0].(*lua.LTable)
	if !ok {
		return provider.Status{}
	}
	return provider.Status{
		Installed: lua.LVAsBool(t.RawGetString("installed")),
		Current:   strOf(t, "current"),
		Healthy:   lua.LVAsBool(t.RawGetString("healthy")),
		Note:      strOf(t, "note"),
	}
}

// Plan calls the plugin's plan() and converts the returned plan table into a
// core.Plan. This is the bridge between the declarative Lua contract and the
// Go apply engine, and the only place the two representations meet.
func (p *LuaProvider) Plan(ctx provider.Context, opts map[string]any) (*core.Plan, error) {
	res, err := p.state.CallMethod(p.mod, "plan",
		ctxTable(p.state.L, ctx), mapToTable(p.state.L, opts))
	if err != nil {
		return nil, fmt.Errorf("plugin %s plan(): %w", p.id, err)
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("plugin %s plan() returned nothing", p.id)
	}
	t, ok := res[0].(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("plugin %s plan() did not return a table", p.id)
	}
	return planFromTable(t, p.id)
}

// Benchmark calls the plugin's benchmark() when it exists.
func (p *LuaProvider) Benchmark(ctx provider.Context, urls []string) ([]provider.Result, error) {
	if p.mod.RawGetString("benchmark") == lua.LNil {
		return nil, nil
	}
	urlTbl := p.state.L.NewTable()
	for i, u := range urls {
		urlTbl.RawSetInt(i+1, lua.LString(u))
	}
	res, err := p.state.CallMethod(p.mod, "benchmark", ctxTable(p.state.L, ctx), urlTbl)
	if err != nil {
		return nil, err
	}
	var out []provider.Result
	if len(res) > 0 {
		if t, ok := res[0].(*lua.LTable); ok {
			t.ForEach(func(_, v lua.LValue) {
				rt, ok := v.(*lua.LTable)
				if !ok {
					return
				}
				out = append(out, provider.Result{
					Name: strOf(rt, "name"),
					URL:  strOf(rt, "url"),
					MS:   int64(lua.LVAsNumber(rt.RawGetString("ms"))),
					OK:   lua.LVAsBool(rt.RawGetString("ok")),
					Err:  strOf(rt, "err"),
				})
			})
		}
	}
	return out, nil
}

// Close releases the plugin's VM.
func (p *LuaProvider) Close() { p.state.Close() }

// ── Lua table → core.Plan ───────────────────────────────────────────────

// planFromTable converts a Lua plan table into a core.Plan.
//
// The recognised shape is:
//
//	{ changes = { { kind = "config", file = "...", format = "ini",
//	                block = "pip", entries = { { section = "global",
//	                                             key = "index-url",
//	                                             value = "..." } } },
//	              { kind = "env", name = "PIP_INDEX_URL", value = "..." } },
//	  warnings = { "..." } }
func planFromTable(t *lua.LTable, providerID string) (*core.Plan, error) {
	b := core.NewBuilder(providerID, presetIDFromOpts(t))

	if ch, ok := t.RawGetString("changes").(*lua.LTable); ok {
		ch.ForEach(func(_, v lua.LValue) {
			ct, ok := v.(*lua.LTable)
			if !ok {
				return
			}
			switch strOf(ct, "kind") {
			case "config":
				entries := []config.Entry{}
				if et, ok := ct.RawGetString("entries").(*lua.LTable); ok {
					et.ForEach(func(_, ev lua.LValue) {
						e, ok := ev.(*lua.LTable)
						if !ok {
							return
						}
						entries = append(entries, config.Entry{
							Section: strOf(e, "section"),
							Key:     strOf(e, "key"),
							Value:   strOf(e, "value"),
							Comment: strOf(e, "comment"),
						})
					})
				}
				b.SetFile(strOf(ct, "file"),
					markerFormatOf(strOf(ct, "format")),
					configFormatOf(strOf(ct, "syntax")),
					strOf(ct, "block"), entries...)

			case "config-remove":
				// Strip the managed block entirely: the restore-official
				// path. Writing an empty value would leave the tool with an
				// invalid config instead of its built-in default.
				b.RemoveFile(strOf(ct, "file"),
					markerFormatOf(strOf(ct, "format")),
					strOf(ct, "block"))

			case "env":
				b.SetEnv(strOf(ct, "name"), strOf(ct, "value"))

			case "env-unset":
				b.UnsetEnv(strOf(ct, "name"))
			}
		})
	}

	if w, ok := t.RawGetString("warnings").(*lua.LTable); ok {
		for i := 1; i <= w.Len(); i++ {
			b.Warn("%s", lua.LVAsString(w.RawGetInt(i)))
		}
	}
	return b.Result(), nil
}

func presetIDFromOpts(t *lua.LTable) string {
	if o, ok := t.RawGetString("opts").(*lua.LTable); ok {
		return strOf(o, "_preset")
	}
	return ""
}

// ── Go → Lua marshalling ────────────────────────────────────────────────

func ctxTable(L *lua.LState, ctx provider.Context) *lua.LTable {
	t := L.NewTable()
	L.SetField(t, "platform", lua.LString(ctx.Platform))
	L.SetField(t, "home", lua.LString(ctx.Home))
	L.SetField(t, "shell", lua.LString(ctx.Shell))
	L.SetField(t, "values", mapToTable(L, ctx.Values))
	return t
}

func mapToTable(L *lua.LState, m map[string]any) *lua.LTable {
	t := L.NewTable()
	for k, v := range m {
		L.SetField(t, k, goToLua(L, v))
	}
	return t
}

func goToLua(L *lua.LState, v any) lua.LValue {
	switch t := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(t)
	case bool:
		if t {
			return lua.LTrue
		}
		return lua.LFalse
	case int:
		return lua.LNumber(t)
	case int64:
		return lua.LNumber(t)
	case float64:
		return lua.LNumber(t)
	case []string:
		tb := L.NewTable()
		for i, s := range t {
			tb.RawSetInt(i+1, lua.LString(s))
		}
		return tb
	case map[string]any:
		return mapToTable(L, t)
	default:
		return lua.LString(fmt.Sprint(t))
	}
}

// tableToMap converts a Lua table to a Go map, one level deep.
func tableToMap(v lua.LValue) map[string]any {
	t, ok := v.(*lua.LTable)
	if !ok {
		return nil
	}
	out := map[string]any{}
	t.ForEach(func(k, val lua.LValue) {
		out[lua.LVAsString(k)] = luaToGo(val)
	})
	return out
}

// luaToGo converts a scalar Lua value to its Go equivalent.
func luaToGo(v lua.LValue) any {
	switch t := v.(type) {
	case lua.LString:
		return string(t)
	case lua.LBool:
		return bool(t)
	case lua.LNumber:
		return float64(t)
	case *lua.LTable:
		return tableToMap(t)
	}
	return nil
}

// strOf reads a string field from a Lua table.
func strOf(t *lua.LTable, key string) string {
	if t == nil {
		return ""
	}
	v := t.RawGetString(key)
	if v == lua.LNil {
		return ""
	}
	return lua.LVAsString(v)
}

// markerFormatOf maps a format name to the marker comment syntax.
func markerFormatOf(s string) markerFormat {
	switch strings.ToLower(s) {
	case "semicolon":
		return markerFormatSemicolon
	case "slash":
		return markerFormatSlash
	case "xml":
		return markerFormatXML
	default:
		return markerFormatHash
	}
}

// configFormatOf maps a syntax name to the config value syntax.
func configFormatOf(s string) config.Format {
	switch strings.ToLower(s) {
	case "toml":
		return config.FormatTOML
	case "kv":
		return config.FormatKV
	case "yaml":
		return config.FormatYAML
	default:
		return config.FormatINI
	}
}
