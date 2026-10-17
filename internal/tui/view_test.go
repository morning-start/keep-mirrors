package tui

import (
	"strings"
	"testing"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/core"
	"keep-mirrors/internal/provider"
	"keep-mirrors/internal/registry"
)

// stub is a Provider with no Lua behind it, so rendering can be tested
// without a VM or a filesystem.
type stub struct {
	id     string
	title  string
	cat    provider.Category
	status provider.Status
	fail   bool
}

func (s *stub) ID() string                        { return s.id }
func (s *stub) Title() string                     { return s.title }
func (s *stub) Category() provider.Category       { return s.cat }
func (s *stub) Version() provider.ContractVersion { return provider.ContractVersion{Major: 1} }
func (s *stub) Platforms() []string               { return nil }
func (s *stub) Detects() []string                 { return nil }
func (s *stub) Envs() []string                    { return nil }
func (s *stub) Settings() []provider.Setting      { return nil }

func (s *stub) Presets() []provider.Preset {
	return []provider.Preset{
		{ID: "tuna", Label: "Tuna", Opts: map[string]any{"index": "https://a.invalid"}},
		{ID: "official", Label: "Official", Opts: map[string]any{"index": "https://b.invalid"}},
	}
}

func (s *stub) Status(provider.Context) provider.Status { return s.status }

func (s *stub) Plan(provider.Context, map[string]any) (*core.Plan, error) {
	if s.fail {
		return nil, errStub
	}
	b := core.NewBuilder(s.id, "tuna")
	b.SetEnv("TEST_INDEX", "https://a.invalid")
	return b.Result(), nil
}

func (s *stub) Benchmark(provider.Context, []string) ([]provider.Result, error) {
	return nil, nil
}

var errStub = stubError("plan exploded")

type stubError string

func (e stubError) Error() string { return string(e) }

func newModel(t *testing.T, ps ...provider.Provider) *app.Model {
	t.Helper()
	reg := registry.New()
	for _, p := range ps {
		reg.Register(p)
	}
	m := app.New(reg)
	m.SetSize(100, 30)
	m.RefreshSelection()
	return m
}

// TestViewSurvivesFailedPlan is the regression test for a nil dereference: the
// detail pane walked m.Plan.Warnings unconditionally, and a failed plan leaves
// m.Plan nil. Rendering in that state used to panic, which in a TUI means the
// whole program dies.
func TestViewSurvivesFailedPlan(t *testing.T) {
	p := &stub{id: "boom", title: "Boom", cat: provider.CategoryLanguage, fail: true}
	m := newModel(t, p)

	if m.Plan != nil {
		t.Fatal("expected a nil plan for a failing provider")
	}
	if m.PlanErr == nil {
		t.Fatal("expected PlanErr to be recorded")
	}

	out := View(m, false, "") // must not panic
	if !strings.Contains(out, "plan exploded") {
		t.Errorf("the plan error is not shown to the user:\n%s", out)
	}
}

// TestViewShowsConfiguredBadge checks that "configured" is rendered for a
// provider whose mirror is set even when its toolchain is not installed.
func TestViewShowsConfiguredBadge(t *testing.T) {
	p := &stub{
		id: "pip", title: "Pip", cat: provider.CategoryLanguage,
		status: provider.Status{Installed: false, Current: "https://a.invalid"},
	}
	m := newModel(t, p)

	out := View(m, false, "")
	if !strings.Contains(out, "已配置") {
		t.Errorf("a configured provider is not shown as configured:\n%s", out)
	}
	if strings.Contains(out, "未安装") {
		t.Errorf("a configured provider is wrongly shown as not installed:\n%s", out)
	}
}

// TestViewRendersFilterPrompt checks the status bar switches to the filter
// prompt while the user is typing.
func TestViewRendersFilterPrompt(t *testing.T) {
	p := &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage}
	m := newModel(t, p)

	out := View(m, true, "pi")
	if !strings.Contains(out, "pi") {
		t.Errorf("the filter draft is not rendered:\n%s", out)
	}
	if !strings.Contains(out, "esc") {
		t.Errorf("the filter prompt does not tell the user how to cancel:\n%s", out)
	}
}

// TestTruncateCountsRunes guards against cutting a Chinese label mid-character,
// which produces a replacement glyph instead of an ellipsis.
func TestTruncateCountsRunes(t *testing.T) {
	got := truncate("清华大学镜像源", 4)
	want := "清华大…"
	if got != want {
		t.Errorf("truncate() = %q, want %q", got, want)
	}
	if n := len([]rune(got)); n != 4 {
		t.Errorf("truncated string is %d runes, want 4", n)
	}
}
