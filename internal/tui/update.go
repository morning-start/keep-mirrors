package tui

import (
	"fmt"
	"strings"
	"time"

	"keep-mirrors/internal/app"

	tea "github.com/charmbracelet/bubbletea"
)

// PollFunc polls the plugin directories and reports what changed. It is
// supplied by the caller so the TUI never learns where plugins live.
type PollFunc func() app.ReloadedMsg

// PollInterval is how often the plugin directories are checked.
//
// One second suits a human editing a file: fast enough that saving feels like
// it took effect, slow enough that the poll is free. Polling is chosen over a
// filesystem watcher because the watched set is a few small text files; an OS
// watcher would add event coalescing, rename semantics and a background
// goroutine's lifetime to get wrong on every platform.
const PollInterval = time.Second

// tickMsg is the poll timer firing; it carries no data.
type tickMsg struct{}

// tickCmd schedules the next poll.
//
// Re-arming from the message handler (rather than tea.Every) keeps exactly one
// timer alive. The cost is that every path returning from Update must forward
// this command, or polling stops silently after one dropped return.
func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

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
	{"g", "顶部", func(m *app.Model) { m.MoveTo(0); m.RefreshSelection() }},
	{"G", "底部", func(m *app.Model) { m.MoveTo(m.Count() - 1); m.RefreshSelection() }},
	{"p", "方案集", cyclePreset},
	{"a", "应用", func(m *app.Model) { _ = m.Apply() }},
	{"u", "撤销", func(m *app.Model) { _ = m.Undo() }},
	{"r", "刷新", func(m *app.Model) { m.RefreshPlan() }},
	{"q", "退出", func(m *app.Model) { m.Quit = true }},
}

// cyclePreset advances the selected provider to its next preset.
func cyclePreset(m *app.Model) {
	p := m.Selected()
	if p == nil || len(p.Presets()) == 0 {
		return
	}
	curl := m.Presets[p.ID()]
	next := p.Presets()[0].ID
	for i, pr := range p.Presets() {
		if pr.ID == curl {
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
	poll          PollFunc
	help          bool
	filtering     bool
	filterDraft   string
	width, height int
}

// New builds the TUI model over an application model. poll may be nil, which
// disables hot reload (tests and any future headless use).
func New(m *app.Model, poll PollFunc) Model {
	return Model{app: m, poll: poll}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	if m.poll == nil {
		return nil
	}
	return tickCmd(PollInterval)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.app.SetSize(msg.Width, msg.Height)
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case app.BenchDoneMsg:
		m.app.ApplyBench(msg)
		return m, nil

	case tickMsg:
		if m.poll == nil {
			return m, nil
		}
		// Run the poll off the UI goroutine, then re-arm the timer
		// immediately. Waiting for the poll to finish before re-arming would
		// stretch the interval by however long loading takes, and a slow
		// reload would make the watcher look dead.
		return m, tea.Batch(
			func() tea.Msg { return m.poll() },
			tickCmd(PollInterval),
		)

	case app.ReloadedMsg:
		m.app.ApplyReload(msg)
		return m, nil

	case tea.KeyMsg:
		s := msg.String()

		if m.help {
			if s == "?" || s == "esc" || s == "q" {
				m.help = false
			}
			return m, nil
		}

		// While the filter prompt is open every keystroke edits the filter
		// instead of triggering a binding, so "t" types a "t".
		if m.filtering {
			return m.updateFilter(s)
		}

		switch s {
		case "?":
			m.help = true
			return m, nil
		case "ctrl+c":
			m.app.Quit = true
			return m, tea.Quit
		case "/":
			m.filtering = true
			m.filterDraft = m.app.Filter
			return m, nil
		case "t":
			return m, m.app.RunBenchmark()
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

// updateFilter handles keys while the filter prompt is open.
func (m Model) updateFilter(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "esc":
		// Abandoning the edit restores the filter that was in force.
		m.filtering = false
		m.filterDraft = ""
		return m, nil
	case "enter":
		m.filtering = false
		m.app.SetFilter(m.filterDraft)
		return m, nil
	case "backspace":
		if m.filterDraft != "" {
			r := []rune(m.filterDraft)
			m.filterDraft = string(r[:len(r)-1])
		}
		return m, nil
	}
	// A single printable character extends the draft; control keys are ignored
	// rather than inserted as their escape sequence.
	if len(s) == 1 && s[0] >= 0x20 {
		m.filterDraft += s
	}
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	if m.help {
		return renderHelp()
	}
	return View(m.app, m.filtering, m.filterDraft)
}

// renderHelp draws the key reference.
func renderHelp() string {
	var b strings.Builder
	b.WriteString("  keep-mirrors 快捷键\n\n")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("  %-4s %s\n", k.key, k.desc))
	}
	b.WriteString("\n  t   测速\n  /   过滤\n  ?   本页\n  esc 关闭\n\n  按任意键返回")
	return b.String()
}

// Run starts the interactive program. poll may be nil to disable hot reload.
func Run(m *app.Model, poll PollFunc) error {
	p := tea.NewProgram(New(m, poll), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
