package tui

import (
	"fmt"
	"strings"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/provider"

	tea "github.com/charmbracelet/bubbletea"
)

// Key bindings. Kept in one table so the help text and the handler cannot
// drift apart.
type key struct {
	key    string
	desc   string
	action func(m *app.Model)
}

var keys = []key{
	{"j", "下移", func(m *app.Model) { m.Move(1); m.RefreshSelection() }},
	{"k", "上移", func(m *app.Model) { m.Move(-1); m.RefreshSelection() }},
	{"g", "顶部", func(m *app.Model) { m.Cursor = 0; m.RefreshSelection() }},
	{"G", "底部", func(m *app.Model) { m.Cursor = len(m.Providers) - 1; m.RefreshSelection() }},
	{"p", "方案集", cyclePreset},
	{"a", "应用", func(m *app.Model) { _ = m.Apply() }},
	{"u", "撤销", func(m *app.Model) { _ = m.Undo() }},
	{"t", "测速", func(m *app.Model) { m.RunBenchmark() }},
	{"r", "刷新", func(m *app.Model) { m.RefreshPlan() }},
	{"q", "退出", func(m *app.Model) { m.Quit = true }},
}

// cyclePreset advances the selected provider to its next preset.
func cyclePreset(m *app.Model) {
	p := m.Selected()
	if p == nil || len(p.Presets()) == 0 {
		return
	}
	cur := m.Presets[p.ID()]
	next := p.Presets()[0].ID
	for i, pr := range p.Presets() {
		if pr.ID == cur {
			next = p.Presets()[(i+1)%len(p.Presets())].ID
			break
		}
	}
	m.SelectPreset(p, next)
}

// Model is the bubbletea model. It wraps app.Model, which owns the state, so
// this file contains only message plumbing.
type Model struct {
	app           *app.Model
	help          bool
	width, height int
}

// New builds the TUI model over an application model.
func New(m *app.Model) Model {
	return Model{app: m}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.app.SetSize(msg.Width, msg.Height)
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		s := msg.String()

		if m.help {
			if s == "?" || s == "esc" || s == "q" {
				m.help = false
			}
			return m, nil
		}

		switch s {
		case "?":
			m.help = true
			return m, nil
		case "ctrl+c":
			m.app.Quit = true
			return m, tea.Quit
		case "/":
			// Enter filter mode.
			return m, filterCmd(m.app)
		}

		for _, k := range keys {
			if k.key == s {
				k.action(m.app)
				if m.app.Quit {
					return m, tea.Quit
				}
				return m, nil
			}
		}
	}
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	if m.help {
		return renderHelp()
	}
	return View(m.app)
}

// renderHelp draws the key reference.
func renderHelp() string {
	var b strings.Builder
	b.WriteString("  keep-mirrors 快捷键\n\n")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("  %-4s %s\n", k.key, k.desc))
	}
	b.WriteString("\n  ?   本页\n  /   过滤\n  esc 关闭\n\n  按任意键返回")
	return b.String()
}

// filterCmd is a placeholder for the filter prompt. Filtering is driven
// directly through the model in v1; this keeps the binding wired without
// pulling in a textinput component yet.
func filterCmd(m *app.Model) tea.Cmd { return nil }

// Run starts the interactive program.
func Run(m *app.Model) error {
	p := tea.NewProgram(New(m), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// ensure provider is referenced so the import survives refactors.
var _ = provider.CategoryLanguage
