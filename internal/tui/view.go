// Package tui renders the interactive interface.
//
// The layout is a two-pane master/detail with a status bar:
//
//	┌ 镜像源 ───────────┬─ 详情 ──────────────────────────┐
//	│ ▸ 语言            │  pip                    [●已配置] │
//	│   ● pip           │  当前: https://pypi.tuna...       │
//	│   ○ npm           │  ── 方案集 ───────────────────   │
//	│   ○ cargo         │  ( ) 清华  (●) 阿里云  ( ) 官方  │
//	│   ○ go            │  ── 变更预览 ─────────────────   │
//	│ ▸ 容器            │  ~/.pip/pip.conf                 │
//	│   ○ docker        │  + index-url = ...               │
//	├───────────────────┴─────────────────────────────────┤
//	│ ● tuna · 2 处待改 · 备份 12:04          [?] 帮助     │
//	└──────────────────────────────────────────────────────┘
//
// Keys are bound in update.go; everything drawn here is a pure function of
// the model.
package tui

import (
	"fmt"
	"strings"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/provider"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── palette ────────────────────────────────────────────────────────────

var (
	colorAccent = lipgloss.Color("39")
	colorMuted  = lipgloss.Color("241")
	colorOK     = lipgloss.Color("42")
	colorWarn   = lipgloss.Color("214")
	colorErr    = lipgloss.Color("203")
	// colorSelBg is a deliberate step away from the background: the old 236
	// (dark grey) was near-invisible on dark themes, and "which row is
	// selected" is the single most important thing a list must communicate.
	colorSelBg = lipgloss.Color("24")

	styleGroup = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	styleItem = lipgloss.NewStyle()

	styleItemSel = lipgloss.NewStyle().
			Bold(true).
			Background(colorSelBg).
			Foreground(lipgloss.Color("231"))

	styleLabel = lipgloss.NewStyle().
			Foreground(colorMuted)

	styleValue = lipgloss.NewStyle().
			Foreground(lipgloss.Color("231"))

	styleHead = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	styleOK    = lipgloss.NewStyle().Foreground(colorOK)
	styleWarn  = lipgloss.NewStyle().Foreground(colorWarn)
	styleErr   = lipgloss.NewStyle().Foreground(colorErr)
	styleMuted = lipgloss.NewStyle().Foreground(colorMuted)

	styleAdd = lipgloss.NewStyle().Foreground(colorOK)
	styleDel = lipgloss.NewStyle().Foreground(colorErr)

	styleBar = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	styleBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted).
			Padding(0, 1)
)

// ── rendering ──────────────────────────────────────────────────────────

// View renders the whole screen. confirming marks a staged plan (the detail
// pane becomes the confirmation panel); filtering/draft drive the filter prompt
// that replaces the status bar while the user is typing.
func View(m *app.Model, confirming, filtering bool, draft string) string {
	if m.Quit {
		return ""
	}
	w, h := m.Width(), m.Height()
	if w <= 0 || h <= 0 {
		return "terminal too small"
	}

	listW := w / 3
	if listW > 34 {
		listW = 34
	}
	if listW < 20 {
		listW = 20
	}
	detailW := w - listW - 1
	if detailW < 10 {
		detailW = 10
	}

	bodyH := h - 1 // the status bar occupies the last line

	left := renderList(m, listW, bodyH)
	var right string
	if confirming {
		right = renderConfirm(m, detailW, bodyH)
	} else {
		right = renderDetail(m, detailW, bodyH)
	}

	// Pad both panes to the same height so the status bar sits on a clean
	// boundary rather than mid-content.
	left = padHeight(left, bodyH)
	right = padHeight(right, bodyH)

	row := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	if filtering {
		return row + "\n" + renderFilterBar(draft, w)
	}
	return row + "\n" + renderBar(m, w, confirming)
}

// renderFilterBar draws the filter prompt in the status bar. The content is
// built to width-2 for the same reason as renderBar: styleBar's horizontal
// padding is added on top of the joined text.
func renderFilterBar(draft string, width int) string {
	label := styleLabel.Render("/ ") + styleValue.Render(draft) + styleMuted.Render("▏")
	hint := styleMuted.Render("enter 确认 · esc 取消")
	inner := width - 2
	gap := inner - lipgloss.Width(label) - lipgloss.Width(hint)
	if gap < 1 {
		gap = 1
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Center, label, strings.Repeat(" ", gap), hint)
	return styleBar.Width(width).Render(bar)
}

// renderList draws the provider tree.
//
// The title and the badge are measured and cut separately: the badge is a
// pre-styled string whose ANSI escape bytes a naive truncate() would count as
// visible text, eating the budget until "已配置" lost its tail. Titles are cut
// by display width so CJK titles stay aligned.
//
// The cursor indexes visible ITEMS; the row counter also counts group headers.
// Comparing the two directly — as this loop once did — highlighted the row
// one header above the provider the detail pane was actually showing.
func renderList(m *app.Model, width, height int) string {
	var b strings.Builder
	statuses := m.AllStatus()

	row := 0  // rendered rows, for the height budget
	item := 0 // visible-item index — the coordinate m.Cursor speaks
	for _, g := range m.Groups() {
		if row >= height {
			break
		}
		b.WriteString(styleGroup.Render(categoryLabel(g.Category)))
		b.WriteString("\n")
		row++

		for _, p := range g.Items {
			if row >= height {
				break
			}
			st := statuses[p.ID()]
			badge := statusBadge(st)

			// The row cursor is ASCII ">" on purpose: the blue bar carries
			// the selection on colour terminals, and the marker keeps it
			// readable on mono ones and in screenshots. A "▸" would render
			// one or two cells wide depending on the terminal's CJK mode and
			// knock every selected row out of alignment.
			marker := "  "
			if item == m.Cursor {
				marker = "> "
			}
			budget := width - 2 - lipgloss.Width(badge) - 1
			if budget < 4 {
				budget = 4
			}
			line := marker + padEnd(ansi.Truncate(p.Title(), budget, "…"), budget) + " " + badge

			if item == m.Cursor {
				// The selection is a full-width bar, not a highlight that
				// stops where the text happens to end.
				b.WriteString(styleItemSel.Render(padEnd(line, width)))
			} else {
				b.WriteString(styleItem.Render(line))
			}
			b.WriteString("\n")
			row++
			item++
		}
	}
	if item == 0 {
		b.WriteString(styleMuted.Render("  (no match)"))
	}
	return b.String()
}

// padEnd pads s with spaces to n display columns. Measured with lipgloss.Width,
// which is ANSI-aware and counts CJK characters as two columns — rune counts
// would leave CJK rows visually shorter than ASCII ones.
func padEnd(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// renderConfirm draws the staged-plan panel: what will be written, for which
// source, and where the official default lies. This is the second of the three
// apply steps (preview → stage → confirm); nothing has touched the disk yet.
func renderConfirm(m *app.Model, width, height int) string {
	p := m.Selected()
	if p == nil {
		return styleMuted.Render("no provider selected")
	}

	var b strings.Builder

	head := styleWarn.Render("⚠ 确认应用") + styleHead.Render(
		" — "+p.Title()+" / "+m.PresetLabel(p))
	b.WriteString(truncate(head, width))
	b.WriteString("\n")

	// The preset's note names the official default when the user is looking
	// at the official preset — "restore official" must say where it restores
	// to, not just that it restores.
	if pr := provider.PresetByID(p.Presets(), m.SelectedPresetID(p)); pr != nil && pr.Note != "" {
		b.WriteString(styleLabel.Render("  官方默认  ") + styleMuted.Render(pr.Note))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleLabel.Render("  将写入以下内容"))
	b.WriteString("\n")
	if m.PlanErr != nil {
		b.WriteString(styleErr.Render("  " + truncate(m.PlanErr.Error(), width-6)))
		b.WriteString("\n")
	} else if m.Plan == nil || m.Plan.Empty() {
		b.WriteString(styleMuted.Render("  (没有待应用的更改)"))
		b.WriteString("\n")
	} else {
		for _, c := range changeViews(m.Plan) {
			for _, l := range renderChange(c, width-6) {
				b.WriteString("  " + l)
				b.WriteString("\n")
			}
		}
	}

	if m.Plan != nil {
		for _, w := range m.Plan.Warnings {
			b.WriteString(styleWarn.Render("  ⚠ " + truncate(w, width-8)))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleOK.Render("  Enter 确认写入") + styleMuted.Render("    Esc 取消，不写入任何配置"))
	b.WriteString("\n")
	return b.String()
}

// renderDetail draws the selected provider's status, presets and diff.
func renderDetail(m *app.Model, width, height int) string {
	p := m.Selected()
	if p == nil {
		return styleMuted.Render("no provider selected")
	}

	var b strings.Builder

	// Title row: name left, status badge right-aligned with real display
	// widths (the old width-20 guess misaligned as soon as a title was longer
	// or shorter than the magic number assumed).
	st := m.Status()
	title := styleHead.Render(p.Title())
	badge := statusBadge(st)
	gap := width - lipgloss.Width(title) - lipgloss.Width(badge) - 2
	if gap < 1 {
		gap = 1
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Center, title, strings.Repeat(" ", gap), badge))
	b.WriteString("\n")

	// Current value.
	if st.Current != "" {
		b.WriteString(styleLabel.Render("  当前  ") + styleValue.Render(truncate(st.Current, width-14)))
		b.WriteString("\n")
		// The official default is the answer to "how do I go back": name it
		// while the mirror is active, not only after the user hunts for it.
		if pr := provider.PresetByID(p.Presets(), "official"); pr != nil && pr.Note != "" {
			b.WriteString(styleLabel.Render("  官方默认  ") + styleMuted.Render(truncate(pr.Note, width-16)))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(styleLabel.Render("  当前  ") + styleMuted.Render("<未配置>"))
		b.WriteString("\n")
	}
	if !st.Installed {
		b.WriteString(styleWarn.Render("  ⚠ 未检测到对应工具链"))
		b.WriteString("\n")
	}

	// Preset picker.
	b.WriteString(styleLabel.Render("  方案集"))
	b.WriteString("\n")
	active := m.PresetLabel(p)
	for _, pr := range p.Presets() {
		marker := "( )"
		s := styleItem
		if pr.Label == active {
			marker = "(●)"
			s = s.Foreground(colorAccent).Bold(true)
		}
		row := s.Render("  " + marker + " " + truncate(pr.Label, width-10))
		if pr.Note != "" {
			// The note rides along muted, so "( ) 官方源" also says where it
			// points (pypi.org, registry.npmjs.org, …) without expanding the
			// picker into a manual.
			row += styleMuted.Render("  — " + truncate(pr.Note, maxInt(6, width-16-lipgloss.Width(pr.Label))))
		}
		b.WriteString(row)
		b.WriteString("\n")
	}

	// Change preview.
	b.WriteString("\n")
	b.WriteString(styleLabel.Render("  变更预览"))
	b.WriteString("\n")
	if m.PlanErr != nil {
		b.WriteString(styleErr.Render("  " + truncate(m.PlanErr.Error(), width-6)))
		b.WriteString("\n")
	} else if m.Plan == nil || m.Plan.Empty() {
		b.WriteString(styleMuted.Render("  (已是当前配置，无需更改)"))
		b.WriteString("\n")
	} else {
		for _, c := range changeViews(m.Plan) {
			for _, l := range renderChange(c, width-6) {
				b.WriteString("  " + l)
				b.WriteString("\n")
			}
		}
	}

	// Benchmark table.
	if m.BenchRunning {
		b.WriteString("\n" + styleMuted.Render("  测速中…"))
		b.WriteString("\n")
	} else if len(m.Bench) > 0 {
		b.WriteString("\n" + styleLabel.Render("  测速结果"))
		b.WriteString("\n")
		for _, r := range benchViews(m.Bench) {
			b.WriteString("  " + renderBench(r, width-4))
			b.WriteString("\n")
		}
	}

	// Warnings. m.Plan is nil whenever the last plan failed, and PlanErr is
	// rendered above in that case, so guard before walking it.
	if m.Plan != nil {
		for _, w := range m.Plan.Warnings {
			b.WriteString(styleWarn.Render("  ⚠ " + truncate(w, width-8)))
			b.WriteString("\n")
		}
	}

	return b.String()
}

// renderChange renders one planned change as one or more diff lines.
//
// Env changes stay on a single line — before → after reads at a glance. Config
// changes expand into the same line diff the headless `km plan` prints (file
// path, then - old / + new), because "+ ~\.config\go\env" told the user nothing
// about what pressing Enter would write.
func renderChange(c ChangeView, width int) []string {
	switch c.Kind {
	case "env":
		return []string{styleLabel.Render("env ") + c.Name +
			"  " + styleDel.Render(c.Before) + " → " + styleAdd.Render(c.After)}
	case "env-unset":
		return []string{styleLabel.Render("env ") + c.Name +
			"  " + styleDel.Render(c.Before) + " → " + styleMuted.Render("<移除>")}
	}

	// config / config-remove: colour each diff line by its sign. The lines
	// come from core.Diff unstyled, so truncating before styling is safe.
	out := make([]string, 0, len(c.Diff))
	for i, l := range c.Diff {
		var style lipgloss.Style
		switch {
		case i == 0:
			style = styleLabel
		case strings.HasPrefix(l, "  +"):
			style = styleAdd
		case strings.HasPrefix(l, "  -"):
			style = styleDel
		default:
			style = styleValue
		}
		out = append(out, style.Render(ansi.Truncate(l, width, "…")))
	}
	if len(out) == 0 {
		out = append(out, styleMuted.Render(ansi.Truncate(c.File, width, "…")))
	}
	return out
}

// renderBench renders one benchmark row.
func renderBench(r BenchView, width int) string {
	if !r.OK {
		return fmt.Sprintf("%-24s %s", truncate(r.Name, 24),
			styleErr.Render("失败"))
	}
	return fmt.Sprintf("%-24s %s", truncate(r.Name, 24),
		styleOK.Render(fmt.Sprintf("%d ms", r.MS)))
}

// barSlack is the safety margin for East-Asian-ambiguous characters (the
// arrows and the · separator). Terminals with a CJK font render them two cells
// wide while lipgloss counts one, so a bar that "fits" by measurement can still
// overflow and wrap. The slack is absorbed by the gap, which is invisible —
// the bar background spans the full width regardless.
const barSlack = 12

// renderBar draws the status line.
//
// The hint shrinks before the line is allowed to wrap: a wrapped status bar
// pushed the whole frame down a line on every render. Note the content is
// built to width-2 because styleBar's horizontal padding is added on top of
// the joined text — filling the full width wrapped the last two columns.
func renderBar(m *app.Model, width int, confirming bool) string {
	left := ""
	if confirming {
		// The staged state has its own footer; a stale status message here
		// would sit next to the confirmation keys and confuse both.
		left = styleWarn.Render("已暂存，等待确认")
	} else if m.StatusMsg != "" {
		s := m.StatusMsg
		if m.StatusErr {
			left = styleErr.Render(s)
		} else {
			left = styleOK.Render(s)
		}
	} else if p := m.Selected(); p != nil {
		// Name the selected source explicitly: the bar is the one place that
		// answers "which row am I on" in words rather than colours.
		if m.Plan != nil && !m.Plan.Empty() {
			left = fmt.Sprintf("%s · %s · %d 处待改", p.ID(), m.PresetLabel(p), m.Plan.ChangeCount())
			if m.LastBackup != nil {
				// The full backup id is noise until there is something to
				// roll back to; "—" taught nobody anything.
				left += " · 可撤销"
			}
		} else {
			left = fmt.Sprintf("%s · %s · 无需更改", p.ID(), m.PresetLabel(p))
		}
	}

	// The hint swaps for the confirmation keys while a plan is staged — that
	// is the one moment the next Enter writes to disk, so the bar must say so.
	hint := styleMuted.Render("↑↓ 选择 · ←→ 方案集 · Enter 暂存 · Ctrl+Z 撤销 · ? 帮助")
	if confirming {
		hint = styleOK.Render("Enter 确认写入 · Esc 取消")
	}
	if lipgloss.Width(left)+lipgloss.Width(hint)+2+barSlack > width {
		hint = styleMuted.Render("? 帮助 · q 退出")
	}
	if lipgloss.Width(left)+lipgloss.Width(hint)+2+barSlack > width {
		// ansi.Truncate because left may carry status colours; counting its
		// escape bytes would cut the visible text short.
		left = ansi.Truncate(left, width-lipgloss.Width(hint)-3-barSlack, "…")
	}

	inner := width - 2 // styleBar's horizontal padding
	gap := inner - lipgloss.Width(left) - lipgloss.Width(hint) - barSlack
	if gap < 1 {
		gap = 1
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Center, left, strings.Repeat(" ", gap), hint)
	return styleBar.Width(width).Render(bar)
}

// ── helpers ────────────────────────────────────────────────────────────

func categoryLabel(c provider.Category) string {
	switch c {
	case provider.CategoryLanguage:
		return "语言"
	case provider.CategoryContainer:
		return "容器"
	case provider.CategoryOS:
		return "操作系统"
	default:
		return "工具"
	}
}

// statusBadge renders the two independent facts a provider reports: whether
// its toolchain is present, and whether a mirror is configured. "Configured"
// wins because that is what the user is actually looking at, and a configured
// pip whose interpreter is off PATH is still configured.
func statusBadge(s provider.Status) string {
	switch {
	case s.Current != "":
		return styleOK.Render("● 已配置")
	case s.Installed:
		return styleMuted.Render("○ 未配置")
	default:
		return styleMuted.Render("○ 未安装")
	}
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:1])
	}
	return string(r[:n-1]) + "…"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// padHeight pads s with blank lines to exactly n lines.
func padHeight(s string, n int) string {
	lines := strings.Split(s, "\n")
	for len(lines) < n {
		lines = append(lines, "")
	}
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
