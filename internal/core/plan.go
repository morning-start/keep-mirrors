// Package core holds the plan/apply model that every provider feeds into.
//
// The central rule is that providers never write to disk. They return a Plan,
// which is a declarative list of Changes, and the Engine executes it. That
// indirection buys three things that would otherwise have to be re-implemented
// per provider:
//
//  1. dry-run and real apply share one code path, so the diff the user reads
//     is exactly what will be written;
//  2. every apply is automatically backed up, so undo is always available;
//  3. the TUI can render any provider's plan without knowing its format.
package core

import (
	"fmt"
	"strings"
	"time"

	"keep-mirrors/internal/config"
	"keep-mirrors/internal/marker"
	"keep-mirrors/internal/paths"
)

// ChangeKind classifies a Change for rendering.
type ChangeKind string

const (
	// ChangeConfig rewrites a managed block in a configuration file.
	ChangeConfig ChangeKind = "config"
	// ChangeEnv sets a user environment variable.
	ChangeEnv ChangeKind = "env"
	// ChangeEnvUnset removes a user environment variable.
	ChangeEnvUnset ChangeKind = "env-unset"
)

// Change is one atomic modification. Before/After are populated by the engine
// from the current on-disk state, so the UI never has to diff anything itself.
type Change struct {
	Kind ChangeKind

	// ChangeConfig fields.
	File    string        // expanded path
	Format  marker.Format // comment syntax of the file
	CF      config.Format // value syntax inside the block
	Block   string        // logical block name, usually the provider id
	Entries []config.Entry

	// ChangeEnv fields.
	Name  string
	Value string

	// Populated by Engine.Describe.
	Before string
	After  string
	// Human-readable one-line summary for the diff view.
	Summary string
}

// Plan is the ordered set of changes a provider proposes.
type Plan struct {
	Provider string
	PresetID string
	Changes  []Change
	Warnings []string
	// Created is when the plan was built, for staleness checks.
	Created time.Time
}

// Empty reports whether the plan would change anything.
func (p *Plan) Empty() bool { return len(p.Changes) == 0 }

// Warn appends a non-fatal note shown to the user before applying.
func (p *Plan) Warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

// ChangeCount returns the number of atomic modifications.
func (p *Plan) ChangeCount() int { return len(p.Changes) }

// Summary renders the plan as a compact multi-line string for logs.
func (p *Plan) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s: %d change(s)", p.Provider, p.PresetID, len(p.Changes))
	for _, c := range p.Changes {
		fmt.Fprintf(&b, "\n  %s", c.Summary)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "\n  ! %s", w)
	}
	return b.String()
}

// Builder is what providers use to accumulate a plan.
type Builder struct {
	plan Plan
}

// NewBuilder returns a Builder for provider/preset.
func NewBuilder(provider, presetID string) *Builder {
	return &Builder{plan: Plan{
		Provider: provider,
		PresetID: presetID,
		Created:  time.Now(),
	}}
}

// SetFile appends a managed-block rewrite. Relative paths resolve against the
// user's home directory.
func (b *Builder) SetFile(file string, f marker.Format, cf config.Format, block string, entries ...config.Entry) *Builder {
	b.plan.Changes = append(b.plan.Changes, Change{
		Kind:    ChangeConfig,
		File:    paths.Expand(file),
		Format:  f,
		CF:      cf,
		Block:   block,
		Entries: entries,
	})
	return b
}

// SetEnv appends an environment variable change.
func (b *Builder) SetEnv(name, value string) *Builder {
	b.plan.Changes = append(b.plan.Changes, Change{
		Kind:  ChangeEnv,
		Name:  name,
		Value: value,
	})
	return b
}

// UnsetEnv appends an environment variable removal.
func (b *Builder) UnsetEnv(name string) *Builder {
	b.plan.Changes = append(b.plan.Changes, Change{
		Kind: ChangeEnvUnset,
		Name: name,
	})
	return b
}

// Warn appends a warning to the plan under construction.
func (b *Builder) Warn(format string, args ...any) *Builder {
	b.plan.Warn(format, args...)
	return b
}

// Result returns the built plan.
func (b *Builder) Result() *Plan { return &b.plan }
