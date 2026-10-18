package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/config"
	"keep-mirrors/internal/core"
	"keep-mirrors/internal/engine"
	"keep-mirrors/internal/marker"
	"keep-mirrors/internal/paths"
	"keep-mirrors/internal/plugins"
	"keep-mirrors/internal/provider"
	"keep-mirrors/internal/registry"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// viewLayoutTestRegistry loads the real seeded plugins inside a sandboxed
// APPDATA, so the layout is exercised against actual plugin data (CJK preset
// labels, long URLs) without touching the developer's machine.
func viewLayoutTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	home := t.TempDir()
	// Registered before the Setenv calls: t.Cleanup runs LIFO, and Reset must
	// read the restored environment or the package stays cached on this home.
	t.Cleanup(paths.Reset)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	// The plugins consult these to locate their configs; inheriting the
	// developer's values would point the plan at real machine files.
	t.Setenv("GOENV", "")
	t.Setenv("CARGO_HOME", "")
	// One source configured via the environment, so both badge states exist.
	t.Setenv("GOPROXY", "https://goproxy.cn,direct")
	paths.Reset()
	engine.SetSeed(plugins.FS)
	loader := engine.NewLoader()
	if _, err := loader.SeedIfEmpty(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	provs, diag, err := loader.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, d := range diag {
		if d.Err != nil {
			t.Fatalf("plugin %s: %v", d.Path, d.Err)
		}
	}
	reg := registry.New()
	for _, p := range provs {
		reg.Register(p)
	}
	return reg
}

// selectProvider moves the cursor to the named provider.
func selectProvider(m *app.Model, id string) {
	for i, p := range m.Providers {
		if p.ID() == id {
			m.Cursor = i
			return
		}
	}
}

// TestViewLayoutOverWidths locks the three layout invariants the first
// screenshot broke, at every terminal size a laptop is likely to have:
//
//  1. the frame never wraps — a status bar that overflows pushed the whole
//     interface down a line and cut the bottom off;
//  2. status badges are never truncated — the badge is a styled string whose
//     escape bytes a naive truncate() counted as text, so "已配置" lost its
//     tail on ordinary terminals;
//  3. the change preview shows the actual diff — "+ ~\.config\go\env" told
//     the user nothing about what Enter would write.
//
// The color profile is forced to TrueColor because bug 2 only exists when
// styles emit escape sequences; the profile tests get by default strips them
// and hides the bug.
func TestViewLayoutOverWidths(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	reg := viewLayoutTestRegistry(t)

	for _, width := range []int{60, 80, 100, 140} {
		t.Run(fmt.Sprintf("w%d", width), func(t *testing.T) {
			const height = 26
			am := app.New(reg)
			am.SetSize(width, height)
			selectProvider(am, "go")
			am.RefreshSelection()

			out := View(am, false, false, "")
			lines := strings.Split(out, "\n")
			if len(lines) != height {
				t.Errorf("frame is %d lines, want %d (the status bar wrapped)", len(lines), height)
			}

			plain := ansi.Strip(out)
			for _, want := range []string{"● 已配置", "○ 未配置"} {
				if !strings.Contains(plain, want) {
					t.Errorf("badge %q is not rendered intact:\n%s", want, plain)
				}
			}
			// On a narrow terminal the long URL legitimately truncates; the
			// invariant is that the preview names the key being written.
			if !strings.Contains(plain, "GOPROXY=") {
				t.Errorf("the preview does not show the value being written:\n%s", plain)
			}
			if width >= 80 && !strings.Contains(plain, "GOPROXY=https://goproxy.cn,direct") {
				t.Errorf("the preview does not show the full new value:\n%s", plain)
			}
		})
	}
}

// TestViewPreviewShowsConfigDiff pins the preview content for a file change at
// the ChangeView level: file path, then the block's - old / + new lines.
func TestViewPreviewShowsConfigDiff(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	reg := viewLayoutTestRegistry(t)

	// A config file whose managed block holds the old value under the same
	// block name the plan writes, so Describe has something real to diff
	// against (blockSlice matches on the block name).
	file := filepath.Join(t.TempDir(), "stubpip.conf")
	old := "# >>> keep-mirrors:stubpip >>>\nindex-url = https://old.invalid\n# <<< keep-mirrors:stubpip <<<\n"
	if err := os.WriteFile(file, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	// A unique id: the seeded registry already has a real "pip", and
	// selectProvider must land on the stub whose plan we control.
	p := &configStub{
		stub: stub{id: "stubpip", title: "Stub Pip", cat: provider.CategoryLanguage},
		file: file,
	}
	am := app.New(reg)
	am.SetSize(100, 26)
	am.Providers = append(am.Providers, p)
	am.Reg.Register(p)
	selectProvider(am, p.ID())
	am.RefreshSelection()
	if s := am.Selected(); s == nil || s.ID() != "stubpip" {
		got := "<nil>"
		if s != nil {
			got = s.ID()
		}
		t.Fatalf("selected = %s, want stubpip (cursor %d)", got, am.Cursor)
	}

	out := ansi.Strip(View(am, false, false, ""))
	if !strings.Contains(out, "index-url = https://old.invalid") {
		t.Errorf("the old value is not shown:\n%s", out)
	}
	if !strings.Contains(out, "index-url = https://new.invalid") {
		t.Errorf("the new value is not shown:\n%s", out)
	}
}

// TestHighlightMatchesSelectedProvider pins the cursor coordinate: m.Cursor
// indexes visible items, but the list loop once compared it against rendered
// ROW numbers — which include the group headers — so the highlighted row was
// one header above the provider the detail pane showed. The screenshot symptom
// was the list highlighting Flutter while the detail showed Go.
func TestHighlightMatchesSelectedProvider(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	reg := viewLayoutTestRegistry(t)

	am := app.New(reg)
	am.SetSize(100, 26)
	selectProvider(am, "go")
	am.RefreshSelection()

	want := am.Selected()
	if want == nil || want.ID() != "go" {
		t.Fatalf("selected = %v, want go", want)
	}

	for _, line := range strings.Split(View(am, false, false, ""), "\n") {
		if strings.Contains(line, "48;5;24") { // styleItemSel's background
			got := ansi.Strip(line)
			if !strings.Contains(got, "Go") {
				t.Errorf("the highlighted row is not Go:\n%s", got)
			}
			if !strings.Contains(got, "> Go") {
				t.Errorf("the highlighted row lacks the > cursor marker:\n%s", got)
			}
			return
		}
	}
	t.Error("no row is highlighted")
}

// configStub is a provider whose plan rewrites a managed block, so the
// preview's config path can be tested without Lua.
type configStub struct {
	file string
	stub
}

func (s *configStub) ID() string { return "stubpip" }

func (s *configStub) Presets() []provider.Preset {
	return []provider.Preset{{ID: "tuna", Label: "Tuna", Opts: map[string]any{}}}
}

func (s *configStub) Plan(provider.Context, map[string]any) (*core.Plan, error) {
	b := core.NewBuilder(s.ID(), "tuna")
	b.SetFile(s.file, marker.Hash, config.FormatINI, "stubpip",
		config.Entry{Section: "global", Key: "index-url", Value: "https://new.invalid"})
	return b.Result(), nil
}
