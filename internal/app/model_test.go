package app

import (
	"strings"
	"testing"

	"keep-mirrors/internal/core"
	"keep-mirrors/internal/provider"
	"keep-mirrors/internal/registry"
)

// fakeProvider is a Provider with no Lua behind it, so the state machine can be
// tested without a VM. It records how it was called, which is what lets a test
// assert that the right provider got refreshed.
type fakeProvider struct {
	id      string
	title   string
	cat     provider.Category
	presets []provider.Preset
	status  provider.Status

	planCalls  int
	lastOpts   map[string]any
	planErr    error
	benchCalls int
}

func (f *fakeProvider) ID() string                   { return f.id }
func (f *fakeProvider) Title() string                { return f.title }
func (f *fakeProvider) Category() provider.Category  { return f.cat }
func (f *fakeProvider) Meta() provider.Meta          { return provider.Meta{Version: "1.0.0"} }
func (f *fakeProvider) Presets() []provider.Preset   { return f.presets }
func (f *fakeProvider) Settings() []provider.Setting { return nil }

func (f *fakeProvider) Status(provider.Context) provider.Status { return f.status }

func (f *fakeProvider) Plan(_ provider.Context, opts map[string]any) (*core.Plan, error) {
	f.planCalls++
	f.lastOpts = opts
	if f.planErr != nil {
		return nil, f.planErr
	}
	index, _ := opts["index"].(string)
	b := core.NewBuilder(f.id, "")
	b.SetEnv("TEST_"+strings.ToUpper(f.id), index)
	return b.Result(), nil
}

func (f *fakeProvider) Benchmark(_ provider.Context, urls []string) ([]provider.Result, error) {
	f.benchCalls++
	out := make([]provider.Result, 0, len(urls))
	for i, u := range urls {
		out = append(out, provider.Result{Name: u, URL: u, MS: int64(len(urls) - i), OK: true})
	}
	return out, nil
}

func fakeRegistry(t *testing.T, ps ...provider.Provider) *registry.Registry {
	t.Helper()
	reg := registry.New()
	for _, p := range ps {
		reg.Register(p)
	}
	return reg
}

func newFake(id string, cat provider.Category, presets ...provider.Preset) *fakeProvider {
	return &fakeProvider{id: id, title: id, cat: cat, presets: presets, status: provider.Status{Installed: true}}
}

func preset(id, label string, opts map[string]any) provider.Preset {
	return provider.Preset{ID: id, Label: label, Opts: opts}
}

// TestPresetDefaultIsFirstPreset locks the invariant that the first declared
// preset is what the user gets when they never touch the picker. Sorting by id
// once silently changed cargo's default and this is what would catch it.
func TestPresetDefaultIsFirstPreset(t *testing.T) {
	p := newFake("cargo", provider.CategoryLanguage,
		preset("rsproxy", "RsProxy", map[string]any{"index": "https://rsproxy.invalid"}),
		preset("official", "Official", map[string]any{"index": "https://official.invalid"}),
	)
	m := New(fakeRegistry(t, p))

	if got := m.PresetLabel(p); got != "RsProxy" {
		t.Errorf("PresetLabel() = %q, want the first declared preset RsProxy", got)
	}
	if got := m.selectedPresetID(p); got != "rsproxy" {
		t.Errorf("selectedPresetID() = %q, want rsproxy", got)
	}
}

// TestSelectPresetRefreshesPlan checks the wiring: choosing a preset must
// produce a plan built from that preset's options, and must record the preset
// id so the backup manifest can name it.
func TestSelectPresetRefreshesPlan(t *testing.T) {
	p := newFake("pip", provider.CategoryLanguage,
		preset("tuna", "TUNA", map[string]any{"index": "https://tuna.invalid"}),
		preset("aliyun", "Aliyun", map[string]any{"index": "https://aliyun.invalid"}),
	)
	m := New(fakeRegistry(t, p))
	m.RefreshSelection()

	m.SelectPreset(p, "aliyun")

	if m.PlanErr != nil {
		t.Fatalf("PlanErr: %v", m.PlanErr)
	}
	if m.Plan == nil {
		t.Fatal("Plan is nil after SelectPreset")
	}
	if m.Plan.PresetID != "aliyun" {
		t.Errorf("Plan.PresetID = %q, want aliyun", m.Plan.PresetID)
	}
	if got := p.lastOpts["index"]; got != "https://aliyun.invalid" {
		t.Errorf("plugin saw index = %v, want the aliyun preset", got)
	}
}

// TestPlanErrorClearsStalePlan is the regression test for a nil dereference:
// the view read m.Plan.Warnings after a failed plan, when m.Plan was nil.
func TestPlanErrorClearsStalePlan(t *testing.T) {
	p := newFake("boom", provider.CategoryLanguage,
		preset("a", "A", map[string]any{"index": "https://a.invalid"}))
	m := New(fakeRegistry(t, p))
	m.RefreshSelection()
	if m.Plan == nil {
		t.Fatal("expected a plan before the failure")
	}

	p.planErr = errFake
	m.RefreshPlan()

	if m.Plan != nil {
		t.Error("Plan was kept after a failed refresh; the view would render a stale diff")
	}
	if m.PlanErr == nil {
		t.Error("PlanErr was not recorded")
	}
}

// TestFilterNarrowsAndResetsCursor checks that filtering shrinks the list, that
// the cursor lands on a valid row, and that "bottom" cannot point past the end
// of the filtered list (the old binding used len(Providers)-1).
func TestFilterNarrowsAndResetsCursor(t *testing.T) {
	ps := []provider.Provider{
		newFake("pip", provider.CategoryLanguage),
		newFake("npm", provider.CategoryLanguage),
		newFake("docker", provider.CategoryContainer),
	}
	m := New(fakeRegistry(t, ps...))
	m.MoveTo(m.Count() - 1)

	m.SetFilter("dock")

	if m.Count() != 1 {
		t.Fatalf("Count() = %d after filtering for dock, want 1", m.Count())
	}
	if p := m.Selected(); p == nil || p.ID() != "docker" {
		t.Fatalf("Selected() = %v, want docker", p)
	}

	m.MoveTo(m.Count() - 1)
	if p := m.Selected(); p == nil || p.ID() != "docker" {
		t.Errorf("bottom of a filtered list selected %v", p)
	}

	m.SetFilter("")
	if m.Count() != 3 {
		t.Errorf("Count() = %d after clearing the filter, want 3", m.Count())
	}
}

// TestApplyBenchRecordsResults checks that a finished measurement is folded
// back into the model on the UI goroutine and that the fastest row is named.
func TestApplyBenchRecordsResults(t *testing.T) {
	p := newFake("pip", provider.CategoryLanguage,
		preset("tuna", "TUNA", map[string]any{"index": "https://a.invalid"}),
		preset("aliyun", "Aliyun", map[string]any{"index": "https://b.invalid"}),
	)
	m := New(fakeRegistry(t, p))
	m.RefreshSelection()

	cmd := m.RunBenchmark()
	if cmd == nil {
		t.Fatal("RunBenchmark returned no command")
	}
	if !m.BenchRunning {
		t.Error("BenchRunning is false while a measurement is in flight")
	}

	msg := cmd()
	done, ok := msg.(BenchDoneMsg)
	if !ok {
		t.Fatalf("benchmark produced %T, want BenchDoneMsg", msg)
	}
	m.ApplyBench(done)

	if m.BenchRunning {
		t.Error("BenchRunning is still true after the results arrived")
	}
	if len(m.Bench) != 2 {
		t.Fatalf("Bench has %d rows, want 2", len(m.Bench))
	}
	// The fake returns descending MS with the first URL slowest, so sorting
	// fastest-first must reverse them.
	if m.Bench[0].MS > m.Bench[1].MS {
		t.Errorf("results are not sorted fastest-first: %+v", m.Bench)
	}
	if m.StatusErr {
		t.Errorf("status reported an error: %s", m.StatusMsg)
	}
	if !strings.Contains(m.StatusMsg, "最快") {
		t.Errorf("status = %q, want the fastest-endpoint message", m.StatusMsg)
	}
}

// TestBenchURLsDedupes checks the endpoint collector: only http(s) string
// values count, and duplicates across presets collapse.
func TestBenchURLsDedupes(t *testing.T) {
	p := newFake("pip", provider.CategoryLanguage,
		preset("a", "A", map[string]any{
			"index": "https://shared.invalid",
			"flag":  true,
			"other": 42,
		}),
		preset("b", "B", map[string]any{"index": "https://shared.invalid"}),
	)
	got := BenchURLs(p)
	if len(got) != 1 || got[0] != "https://shared.invalid" {
		t.Errorf("BenchURLs() = %v, want the single deduped https endpoint", got)
	}
}

var errFake = fakeError("plugin exploded")

type fakeError string

func (e fakeError) Error() string { return string(e) }
