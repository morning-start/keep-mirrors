// Package app holds the application state machine that sits between the
// Lua plugins and the TUI.
//
// The TUI is a pure renderer: it never touches the filesystem, the registry or
// the environment. Every action the user takes becomes a Msg, and every Msg is
// handled here. That split is what makes the tool testable — the state machine
// can be driven directly in tests without a terminal — and it is what will
// let a headless mode reuse the same logic later.
package app

import (
	"fmt"
	"runtime"
	"sort"
	"strings"

	"keep-mirrors/internal/core"
	"keep-mirrors/internal/env"
	"keep-mirrors/internal/paths"
	"keep-mirrors/internal/provider"
	"keep-mirrors/internal/registry"

	tea "github.com/charmbracelet/bubbletea"
)

// Model is the whole application state.
type Model struct {
	Reg       *registry.Registry
	Providers []provider.Provider

	// Cursor position in the flattened, category-grouped list.
	Cursor int
	// Filter is the current fuzzy filter text.
	Filter string

	// Values are the per-provider option overrides, keyed by provider id.
	Values map[string]map[string]any
	// Presets are the per-provider selected preset ids.
	Presets map[string]string

	// Plan is the pending plan for the selected provider.
	Plan *core.Plan
	// PlanErr is the last plan error, shown in the detail pane.
	PlanErr error

	// Bench holds the latest benchmark results for the selected provider.
	Bench []BenchRow
	// BenchRunning is true while a benchmark is in flight.
	BenchRunning bool

	// LastBackup is the most recent successful backup, for undo.
	LastBackup *core.Backup
	// StatusMsg is the transient message shown in the status bar.
	StatusMsg string
	// StatusErr marks StatusMsg as an error (rendered in red).
	StatusErr bool

	// Quit is set when the user asked to exit.
	Quit bool

	width, height int
}

// New builds the initial model from a registry.
func New(reg *registry.Registry) *Model {
	m := &Model{
		Reg:     reg,
		Values:  map[string]map[string]any{},
		Presets: map[string]string{},
		width:   80,
		height:  24,
	}
	m.Providers = reg.All()
	return m
}

// Ctx returns the provider context passed to plugins.
func (m *Model) Ctx() provider.Context {
	return provider.Context{
		Platform: runtime.GOOS,
		Home:     paths.Home(),
		Shell:    string(env.DetectShell()),
		Values:   map[string]any{},
	}
}

// Selected returns the provider under the cursor.
func (m *Model) Selected() provider.Provider {
	items := m.visible()
	if len(items) == 0 || m.Cursor >= len(items) {
		return nil
	}
	return items[m.Cursor]
}

// visible returns the providers matching the current filter.
func (m *Model) visible() []provider.Provider {
	if m.Filter == "" {
		return m.Providers
	}
	f := strings.ToLower(m.Filter)
	var out []provider.Provider
	for _, p := range m.Providers {
		hay := strings.ToLower(p.ID() + " " + p.Title() + " " + string(p.Category()))
		if strings.Contains(hay, f) {
			out = append(out, p)
		}
	}
	return out
}

// Groups returns the visible providers grouped by category, preserving the
// registry's category order.
func (m *Model) Groups() []Group {
	var out []Group
	for _, cat := range m.Reg.Categories() {
		var items []provider.Provider
		for _, p := range m.visible() {
			if p.Category() == cat {
				items = append(items, p)
			}
		}
		if len(items) > 0 {
			out = append(out, Group{Category: cat, Items: items})
		}
	}
	return out
}

// Group is one category section in the list view.
type Group struct {
	Category provider.Category
	Items    []provider.Provider
}

// Move moves the cursor by delta, clamped to the visible list.
func (m *Model) Move(delta int) {
	n := len(m.visible())
	if n == 0 {
		m.Cursor = 0
		return
	}
	m.Cursor += delta
	if m.Cursor < 0 {
		m.Cursor = 0
	}
	if m.Cursor >= n {
		m.Cursor = n - 1
	}
}

// MoveTo moves the cursor to an absolute index, clamped to the visible list.
// It exists so "bottom" lands on the last *visible* row when a filter is
// active; using len(Providers)-1 would point past the end of the filtered list.
func (m *Model) MoveTo(i int) {
	n := len(m.visible())
	if n == 0 {
		m.Cursor = 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	m.Cursor = i
}

// Count returns the number of visible providers.
func (m *Model) Count() int { return len(m.visible()) }

// SetFilter applies a filter and resets the cursor, which may now point past
// the end of the shorter list.
func (m *Model) SetFilter(f string) {
	m.Filter = f
	m.Cursor = 0
	m.RefreshSelection()
}

// optsFor returns the effective options for a provider: preset values
// overlaid with the user's overrides.
func (m *Model) optsFor(p provider.Provider) map[string]any {
	base := map[string]any{}
	if id := m.Presets[p.ID()]; id != "" {
		if pr := provider.PresetByID(p.Presets(), id); pr != nil {
			for k, v := range pr.Opts {
				base[k] = v
			}
		}
	} else if len(p.Presets()) > 0 {
		for k, v := range p.Presets()[0].Opts {
			base[k] = v
		}
	}
	// Defaults from settings, so a provider with no preset still works.
	for _, s := range p.Settings() {
		if _, ok := base[s.Key]; !ok && s.Default != nil {
			base[s.Key] = s.Default
		}
	}
	for k, v := range m.Values[p.ID()] {
		base[k] = v
	}
	return base
}

// SelectPreset records the chosen preset for a provider and refreshes the plan.
func (m *Model) SelectPreset(p provider.Provider, presetID string) {
	m.Presets[p.ID()] = presetID
	m.refreshPlan()
}

// SetValue records a single option override and refreshes the plan.
func (m *Model) SetValue(p provider.Provider, key string, value any) {
	if m.Values[p.ID()] == nil {
		m.Values[p.ID()] = map[string]any{}
	}
	m.Values[p.ID()][key] = value
	m.refreshPlan()
}

// refreshPlan rebuilds the plan for the selected provider. It is called after
// every edit so the diff pane is never stale relative to the form.
//
// A failure clears m.Plan as well as setting PlanErr: leaving the previous
// plan in place would show a diff for a configuration that no longer matches
// what the form says.
func (m *Model) refreshPlan() {
	p := m.Selected()
	if p == nil {
		m.Plan = nil
		m.PlanErr = nil
		return
	}
	opts := m.optsFor(p)
	plan, err := p.Plan(m.Ctx(), opts)
	if err != nil {
		m.PlanErr = err
		m.Plan = nil
		return
	}
	// The preset id is recorded so the backup manifest and the status bar can
	// name what was applied, not just which provider.
	plan.PresetID = m.selectedPresetID(p)
	if err := plan.Describe(); err != nil {
		m.PlanErr = err
		m.Plan = nil
		return
	}
	m.PlanErr = nil
	m.Plan = plan
}

// selectedPresetID returns the preset the user picked, or the first one, which
// is what the picker shows when nothing was chosen.
func (m *Model) selectedPresetID(p provider.Provider) string {
	if id := m.Presets[p.ID()]; id != "" {
		return id
	}
	if len(p.Presets()) > 0 {
		return p.Presets()[0].ID
	}
	return ""
}

// RefreshSelection recomputes the plan for the newly selected provider and
// clears the benchmark, which belongs to the previous selection.
func (m *Model) RefreshSelection() {
	m.Bench = nil
	m.BenchRunning = false
	m.refreshPlan()
}

// RefreshPlan re-runs the plan for the current selection. Exported so the TUI
// can refresh after an external change without knowing how plans are built.
func (m *Model) RefreshPlan() { m.refreshPlan() }

// BenchRow is one benchmark result, flattened for display. It deliberately
// does not expose provider.Result so the view layer stays decoupled from the
// provider package.
type BenchRow struct {
	Name string
	URL  string
	MS   int64
	OK   bool
	Err  string
}

// Status returns the current on-disk status of the selected provider.
func (m *Model) Status() provider.Status {
	p := m.Selected()
	if p == nil {
		return provider.Status{}
	}
	return p.Status(m.Ctx())
}

// AllStatus returns the status of every provider, for the list badges.
func (m *Model) AllStatus() map[string]provider.Status {
	out := make(map[string]provider.Status, len(m.Providers))
	for _, p := range m.Providers {
		out[p.ID()] = p.Status(m.Ctx())
	}
	return out
}

// Apply executes the pending plan.
func (m *Model) Apply() error {
	if m.Plan == nil || m.Plan.Empty() {
		return fmt.Errorf("nothing to apply")
	}
	bak, err := core.Apply(m.Plan)
	if err != nil {
		m.setErr("apply failed: %v", err)
		return err
	}
	if bak != nil {
		m.LastBackup = bak
	}
	m.setOK("applied %s/%s — backup %s",
		m.Plan.Provider, m.Plan.PresetID, shortID(bak))
	m.refreshPlan()
	return nil
}

// Undo restores the last backup.
func (m *Model) Undo() error {
	if m.LastBackup == nil {
		return fmt.Errorf("nothing to undo")
	}
	if err := m.LastBackup.Restore(); err != nil {
		m.setErr("undo failed: %v", err)
		return err
	}
	id := shortID(m.LastBackup)
	m.LastBackup = nil
	m.setOK("restored %s", id)
	m.refreshPlan()
	return nil
}

// RunBenchmark measures every preset endpoint of the selected provider.
//
// The goroutine does not touch the model. It writes into a channel that
// update.go drains as a tea.Cmd, so the mutation happens on the UI goroutine
// where the rest of the state lives. Writing m.Bench directly from here races
// with View reading it.
func (m *Model) RunBenchmark() tea.Cmd {
	p := m.Selected()
	if p == nil {
		return nil
	}
	m.BenchRunning = true
	m.Bench = nil

	urls := BenchURLs(p)
	if len(urls) == 0 {
		m.BenchRunning = false
		m.setErr("%s has no benchmarkable presets", p.ID())
		return nil
	}

	ctx := m.Ctx()
	return func() tea.Msg {
		res, err := p.Benchmark(ctx, urls)
		return BenchDoneMsg{rows: toBenchRows(res), err: err}
	}
}

// BenchDoneMsg carries a finished measurement back to the UI goroutine.
type BenchDoneMsg struct {
	rows []BenchRow
	err  error
}

// ApplyBench records the results of a finished measurement.
func (m *Model) ApplyBench(msg BenchDoneMsg) {
	m.BenchRunning = false
	if msg.err != nil {
		m.setErr("benchmark failed: %v", msg.err)
		return
	}
	m.Bench = msg.rows
	if fastest := rowsFastest(m.Bench); fastest != nil {
		m.setOK("最快：%s（%d ms）", fastest.Name, fastest.MS)
	} else {
		m.setErr("所有镜像均不可达")
	}
}

// BenchURLs collects the distinct endpoints across every preset of p.
func BenchURLs(p provider.Provider) []string {
	seen := map[string]bool{}
	var urls []string
	for _, pr := range p.Presets() {
		for _, v := range pr.Opts {
			s, ok := v.(string)
			if !ok || !strings.HasPrefix(s, "http") || seen[s] {
				continue
			}
			seen[s] = true
			urls = append(urls, s)
		}
	}
	return urls
}

// toBenchRows sorts results fastest-first and flattens them for the view.
func toBenchRows(res []provider.Result) []BenchRow {
	sort.SliceStable(res, func(i, j int) bool {
		if res[i].OK != res[j].OK {
			return res[i].OK
		}
		return res[i].MS < res[j].MS
	})
	rows := make([]BenchRow, 0, len(res))
	for _, r := range res {
		rows = append(rows, BenchRow{Name: r.Name, URL: r.URL, MS: r.MS, OK: r.OK, Err: r.Err})
	}
	return rows
}

func rowsFastest(rows []BenchRow) *BenchRow {
	for i := range rows {
		if rows[i].OK {
			return &rows[i]
		}
	}
	return nil
}

func (m *Model) setOK(format string, args ...any) {
	m.StatusMsg = fmt.Sprintf(format, args...)
	m.StatusErr = false
}

func (m *Model) setErr(format string, args ...any) {
	m.StatusMsg = fmt.Sprintf(format, args...)
	m.StatusErr = true
}

// SetSize records the terminal size.
func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

// Width and Height expose the terminal size to the view.
func (m *Model) Width() int  { return m.width }
func (m *Model) Height() int { return m.height }

// shortID trims a backup id for display in the status bar.
func shortID(b *core.Backup) string {
	if b == nil {
		return "none"
	}
	return b.ID
}

// PresetLabel returns the label of the active preset, or "custom".
func (m *Model) PresetLabel(p provider.Provider) string {
	if id := m.Presets[p.ID()]; id != "" {
		if pr := provider.PresetByID(p.Presets(), id); pr != nil {
			return pr.Label
		}
	}
	if len(p.Presets()) > 0 {
		return p.Presets()[0].Label
	}
	return "custom"
}
