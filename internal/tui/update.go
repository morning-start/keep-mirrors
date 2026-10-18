package tui

import (
	"fmt"
	"strings"
	"time"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/provider"

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
//
// Every binding takes its canonical key first (arrow keys, enter, esc) and
// keeps the vim-style letter as an alias. The canonical key is what a first
// time user tries; the alias is muscle memory for people who already have it.
// The help screen shows both, but the status-bar hint leads with the arrow
// keys so nobody has to guess that j/k is the movement mechanism.
type key struct {
	keys   []string
	desc   string
	action func(m *app.Model)
}

// pageSize is how far page-up/page-down travel. The list is short (one row per
// plugin), so a full screen would fly past the end; ten rows is roughly what
// fits and still feels like a page.
const pageSize = 10

var keys = []key{
	{[]string{"down", "j"}, "下移", func(m *app.Model) { m.Move(1); m.RefreshSelection() }},
	{[]string{"up", "k"}, "上移", func(m *app.Model) { m.Move(-1); m.RefreshSelection() }},
	{[]string{"home", "g"}, "顶部", func(m *app.Model) { m.MoveTo(0); m.RefreshSelection() }},
	{[]string{"end", "G"}, "底部", func(m *app.Model) { m.MoveTo(m.Count() - 1); m.RefreshSelection() }},
	{[]string{"pgdown"}, "下翻页", func(m *app.Model) { m.Move(pageSize); m.RefreshSelection() }},
	{[]string{"pgup"}, "上翻页", func(m *app.Model) { m.Move(-pageSize); m.RefreshSelection() }},
	{[]string{"left", "h"}, "上一方案集", func(m *app.Model) { cyclePreset(m, -1) }},
	{[]string{"right", "l"}, "下一方案集", func(m *app.Model) { cyclePreset(m, 1) }},
	{[]string{"p"}, "方案集", func(m *app.Model) { cyclePreset(m, 1) }},
	{[]string{"ctrl+z", "u"}, "撤销", func(m *app.Model) { _ = m.Undo() }},
	{[]string{"r"}, "刷新", func(m *app.Model) { m.RefreshPlan() }},
	{[]string{"q"}, "退出", func(m *app.Model) { m.Quit = true }},
}

// cyclePreset advances the selected provider's preset by delta, wrapping.
// Going "left" on the first preset wraps to the last, which is what makes the
// arrow keys feel symmetric instead of dead-ending.
func cyclePreset(m *app.Model, delta int) {
	p := m.Selected()
	if p == nil || len(p.Presets()) == 0 {
		return
	}
	presets := p.Presets()
	idx := 0
	if cur := m.Presets[p.ID()]; cur != "" {
		for i, pr := range presets {
			if pr.ID == cur {
				idx = i
				break
			}
		}
	}
	n := len(presets)
	next := ((idx+delta)%n + n) % n
	m.SelectPreset(p, presets[next].ID)
}

// Model is the bubbletea model. It wraps app.Model, which owns the state, so
// this file contains only message plumbing.
type Model struct {
	app  *app.Model
	poll PollFunc
	help bool
	// confirming marks a staged plan: the diff is on screen and nothing has
	// been written. Only Enter/y and Esc/n leave this state.
	confirming    bool
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

		// While the confirmation panel is open the plan is staged: every key
		// except the ones that confirm or cancel is swallowed, so a stray
		// ↓ or Enter typed while reading the diff cannot write anything.
		if m.confirming {
			return m.updateConfirm(s)
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
		case "enter", "a":
			// Nothing is written here. Staging opens the confirmation panel
			// with the full diff; only a second explicit Enter applies.
			return m.stagePlan()
		case "o":
			return m.stageOfficial()
		}

		for _, k := range keys {
			if hasKey(k.keys, s) {
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

// stagePlan opens the confirmation panel for the pending plan. Staging is the
// whole safety story: the first Enter must never write, because on a keyboard
// the confirm key and the navigation keys are neighbours.
func (m Model) stagePlan() (tea.Model, tea.Cmd) {
	if m.app.Plan == nil || m.app.Plan.Empty() {
		m.app.StatusMsg = "已是当前配置，无需更改"
		m.app.StatusErr = false
		return m, nil
	}
	m.confirming = true
	return m, nil
}

// stageOfficial selects the provider's official preset — the way back to the
// built-in default — and opens the confirmation panel for it, so reverting is
// one deliberate keypress instead of cycling through the picker.
func (m Model) stageOfficial() (tea.Model, tea.Cmd) {
	p := m.app.Selected()
	if p == nil {
		return m, nil
	}
	if provider.PresetByID(p.Presets(), "official") == nil {
		m.app.StatusMsg = p.Title() + " 没有官方源方案集"
		m.app.StatusErr = true
		return m, nil
	}
	m.app.SelectPreset(p, "official")
	if m.app.Plan == nil || m.app.Plan.Empty() {
		m.app.StatusMsg = "已是官方默认配置"
		m.app.StatusErr = false
		return m, nil
	}
	m.confirming = true
	return m, nil
}

// updateConfirm handles keys while a plan is staged for confirmation.
func (m Model) updateConfirm(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "enter", "y":
		m.confirming = false
		_ = m.app.Apply()
		return m, nil
	case "esc", "n":
		m.confirming = false
		// Silence, not an error: cancelling is the user doing it right.
		m.app.StatusMsg = "已取消，未写入任何配置"
		m.app.StatusErr = false
		return m, nil
	case "ctrl+c":
		m.app.Quit = true
		return m, tea.Quit
	}
	// Every other key is swallowed: the staged diff must not change out from
	// under the Enter the user is about to press.
	return m, nil
}

// hasKey reports whether s matches any of the binding's key names.
func hasKey(names []string, s string) bool {
	for _, n := range names {
		if n == s {
			return true
		}
	}
	return false
}

// updateFilter handles keys while the filter prompt is open.
func (m Model) updateFilter(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "esc":
		// Abandoning the edit restores the filter that was in force.
		m.filtering = false
		m.filterDraft = ""
		return m, nil
	case "ctrl+c":
		// Quitting must stay reachable while typing. The filter prompt
		// otherwise swallows every key, including the one that exits, so a
		// user who opened it by accident cannot get back to a state where
		// ctrl+c works.
		m.app.Quit = true
		return m, tea.Quit
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
	//
	// Counted in runes, not bytes: a Chinese character is one keystroke but
	// three bytes, so a len(s)==1 test silently swallowed every CJK entry and
	// made the filter look broken to anyone typing in Chinese.
	if r := []rune(s); len(r) == 1 && r[0] >= 0x20 && r[0] != 0x7f {
		m.filterDraft += string(r)
	}
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	if m.help {
		return renderHelp()
	}
	return View(m.app, m.confirming, m.filtering, m.filterDraft)
}

// renderHelp draws the key reference.
//
// The canonical key is printed large and the aliases follow in parentheses, so
// the first column answers "what do I press" for a new user rather than
// advertising vim bindings as if they were the interface.
func renderHelp() string {
	var b strings.Builder
	b.WriteString("  keep-mirrors 快捷键\n\n")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("  %-14s %s\n", formatKeys(k.keys), k.desc))
	}
	b.WriteString(fmt.Sprintf("  %-14s %s\n", "Enter", "暂存方案，进入确认"))
	b.WriteString(fmt.Sprintf("  %-14s %s\n", "o", "恢复官方源（进入确认）"))
	b.WriteString(fmt.Sprintf("  %-14s %s\n", "t", "测速"))
	b.WriteString(fmt.Sprintf("  %-14s %s\n", "/", "过滤"))
	b.WriteString(fmt.Sprintf("  %-14s %s\n", "?", "本页"))
	b.WriteString(fmt.Sprintf("  %-14s %s\n", "esc", "取消 / 关闭"))
	b.WriteString("\n  应用走 预览 → 暂存 → 确认 三步：Enter 暂存后显示完整差异，\n  再按 Enter 才写入，Esc 随时取消。\n\n  方向键即可上下移动；j/k 等字母键同样可用。\n\n  按任意键返回")
	return b.String()
}

// formatKeys renders a binding's key list: the canonical key first, aliases in
// parentheses.
func formatKeys(names []string) string {
	if len(names) == 0 {
		return ""
	}
	head := keyLabel(names[0])
	if len(names) == 1 {
		return head
	}
	aliases := make([]string, 0, len(names)-1)
	for _, n := range names[1:] {
		aliases = append(aliases, keyLabel(n))
	}
	return head + " (" + strings.Join(aliases, ", ") + ")"
}

// keyLabel makes bubbletea's key names readable. "pgdown" is not a label; "PgDn"
// is.
func keyLabel(name string) string {
	switch name {
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	case "pgup":
		return "PgUp"
	case "pgdown":
		return "PgDn"
	case "home":
		return "Home"
	case "end":
		return "End"
	case "enter":
		return "Enter"
	case "ctrl+z":
		return "Ctrl+Z"
	case "esc":
		return "Esc"
	}
	return name
}

// Run starts the interactive program. poll may be nil to disable hot reload.
func Run(m *app.Model, poll PollFunc) error {
	p := tea.NewProgram(New(m, poll), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
