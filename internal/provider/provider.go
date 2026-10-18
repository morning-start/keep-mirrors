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

// Meta is the supplementary metadata a plugin publishes about itself.
//
// It is deliberately kept off the Provider interface. The three identity
// methods there (ID, Title, Category) are read while rendering the list, which
// happens on every keystroke; the fields here are read when a user opens the
// detail pane, lists what is installed, or reports a bug. Grouping them means
// adding a field never churns the interface or every implementation of it.
type Meta struct {
	// Version is the plugin's own release, independent of API.
	//
	// The two move for different reasons and must not be conflated: changing
	// a mirror URL bumps Version, while using a new km.* capability bumps
	// API. Only API is a load-time gate.
	Version string

	// API is the contract version the plugin was written against.
	API ContractVersion

	// Author, License and Homepage are expected of anything distributed
	// publicly. They exist so a bug report can find the plugin's origin.
	Author   string
	License  string
	Homepage string

	// Description is one line, shown in the detail pane.
	Description string

	// Platforms lists the GOOS values the plugin claims to support. Empty
	// means "any"; it is advisory and does not gate loading.
	Platforms []string
	// Detects lists executables used to decide whether the toolchain is
	// present. The UI reads it; the plugin makes the actual decision.
	Detects []string
	// Envs lists the environment variables the plugin may touch.
	Envs []string
}

// Preset is a named, one-click configuration.
type Preset struct {
	ID    string
	Label string
	Opts  map[string]any
	// Note is an optional one-liner shown beside the preset — the official
	// preset uses it to name the built-in default (e.g. "pypi.org"), which is
	// what tells a user where "restore the official source" actually points.
	Note string
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
	// Meta returns the supplementary metadata, read on demand rather than on
	// every render.
	Meta() Meta
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

// PresetByID returns the preset with the given id, or nil. Declaration order
// is preserved by the callers, so this is a lookup and never a re-sort.
func PresetByID(ps []Preset, id string) *Preset {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}
