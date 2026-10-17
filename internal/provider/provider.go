// Package provider defines the contract every mirror source implements.
//
// The shape below is deliberately the Lua contract from the design notes,
// mirrored 1:1 in Go types. When the Lua engine lands, a plugin module is
// loaded into a struct that satisfies this same interface, and the TUI cannot
// tell the difference. That is why the fields look declarative (Presets,
// Settings) rather than behavioural: they are data the UI renders, not logic
// the UI understands.
package provider

import (
	"keep-mirrors/internal/core"
)

// Category groups providers in the UI list.
type Category string

const (
	CategoryLanguage  Category = "language"
	CategoryContainer Category = "container"
	CategoryOS        Category = "os"
	CategoryTool      Category = "tool"
)

// ContractVersion is the plugin contract this binary implements. A plugin
// declaring a different major version is refused.
type ContractVersion struct {
	Major int
	Minor int
}

// Preset is a named, one-click configuration.
type Preset struct {
	ID    string
	Label string
	Opts  map[string]any
}

// Setting describes one editable option. The UI renders a control from this
// and hands the value back as opts[key], so providers never see the widget.
type Setting struct {
	Key      string
	Type     string // "string" | "bool" | "choice" | "int"
	Label    string
	Default  any
	Choices  []string
	Validate string // "url" | "path" | "host" | ""
	Help     string
}

// Status is what the list view shows for a provider.
type Status struct {
	Installed bool
	Current   string
	Healthy   bool
	Note      string
}

// Context carries everything a provider needs to know about the host.
type Context struct {
	Platform string // "windows" | "linux" | "darwin"
	Home     string
	Shell    string
	// Values is the current opts (preset merged with user overrides).
	Values map[string]any
}

// Provider is one mirror source.
type Provider interface {
	// ID is the stable identifier used as the managed-block name.
	ID() string
	Title() string
	Category() Category
	Version() ContractVersion
	Platforms() []string
	Detects() []string
	Envs() []string
	Presets() []Preset
	Settings() []Setting

	// Status reports the current on-disk state.
	Status(ctx Context) Status
	// Plan builds the change set for opts.
	Plan(ctx Context, opts map[string]any) (*core.Plan, error)
	// Benchmark measures candidate endpoints, when the provider supports it.
	Benchmark(ctx Context, urls []string) ([]Result, error)
}

// BenchmarkResult is one measurement.
type Result struct {
	Name string
	URL  string
	MS   int64
	OK   bool
	Err  string
}

// Base carries the metadata common to most providers, so a concrete provider
// only implements Status, Plan and (optionally) Benchmark.
type Base struct {
	IDv        string
	Titlev     string
	Categoryv  Category
	PlatformsV []string
	DetectsV   []string
	EnvsV      []string
	PresetsV   []Preset
	SettingsV  []Setting
}

func (b Base) ID() string               { return b.IDv }
func (b Base) Title() string            { return b.Titlev }
func (b Base) Category() Category       { return b.Categoryv }
func (b Base) Version() ContractVersion { return ContractVersion{Major: 1, Minor: 0} }
func (b Base) Platforms() []string      { return b.PlatformsV }
func (b Base) Detects() []string        { return b.DetectsV }
func (b Base) Envs() []string           { return b.EnvsV }
func (b Base) Presets() []Preset        { return b.PresetsV }
func (b Base) Settings() []Setting      { return b.SettingsV }

// Str coerces an opts value to a string, tolerating nil.
func Str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// Bool coerces an opts value to a bool, tolerating nil.
func Bool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1" || t == "yes"
	}
	return false
}

// Get returns opts[key] or the setting's default when absent.
func Get(opts map[string]any, def map[string]any, key string) any {
	if v, ok := opts[key]; ok && v != nil {
		return v
	}
	if v, ok := def[key]; ok {
		return v
	}
	return nil
}
