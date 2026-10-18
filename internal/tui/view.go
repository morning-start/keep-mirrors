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
)

// ── palette ────────────────────────────────────────────────────────────

var (
	colorAccent = lipgloss.Color("39")
	colorMuted  = lipgloss.Color("241")
	colorOK     = lipgloss.Color("42")
	colorWarn   = lipgloss.Color("214")
	colorErr    = lipgloss.Color("203")
	colorSelBg  = lipgloss.Color("236")

	styleGroup = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	styleItem = lipgloss.NewStyle().
			PaddingLeft(2)

	styleItemSel = lipgloss.NewStyle().
			PaddingLeft(2).
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

// View renders the whole screen. filtering/draft drive the filter prompt that
// replaces the status bar while the user is typing.
func View(m *app.Model, filtering bool, draft string) string {
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

	bodyH := h - 2 // status bar + one blank line

	left := renderList(m, listW, bodyH)
	right := renderDetail(m, detailW, bodyH)

	// Pad both panes to the same height so the status bar sits on a clean
	// boundary rather than mid-content.
	left = padHeight(left, bodyH)
	right = padHeight(right, bodyH)

	row := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	if filtering {
		return row + "\n" + renderFilterBar(draft, w)
	}
	return row + "\n" + renderBar(m, w)
}

// renderFilterBar draws the filter prompt in the status bar.
func renderFilterBar(draft string, width int) string {
	label := styleLabel.Render("/ ") + styleValue.Render(draft) + styleMuted.Render("▏")
	hint := styleMuted.Render("enter 确认 · esc 取消")
	bar := lipgloss.JoinHorizontal(lipgloss.Center, label,
		strings.Repeat(" ", maxInt(1, width-lipgloss.Width(label)-lipgloss.Width(hint))), hint)
	return styleBar.Width(width).Render(bar)
}

// renderList draws the provider tree.
func renderList(m *app.Model, width, height int) string {
	var b strings.Builder
	statuses := m.AllStatus()

	i := 0
	for _, g := range m.Groups() {
		if i >= height-2 {
			break
		}
		b.WriteString(styleGroup.Render(categoryLabel(g.Category)))
		b.WriteString("\n")
		i++

		for _, p := range g.Items {
			if i >= height-2 {
				break
			}
			st := statuses[p.ID()]
			badge := statusBadge(st)
			line := fmt.Sprintf("%-14s %s", truncate(p.Title(), 14), badge)

			if i == m.Cursor {
				b.WriteString(styleItemSel.Render(truncate(line, width-2)))
			} else {
				b.WriteString(styleItem.Render(truncate(line, width-2)))
			}
			b.WriteString("\n")
			i++
		}
	}
	if i == 0 {
		b.WriteString(styleMuted.Render("  (no match)"))
	}
	return b.String()
}

// renderDetail draws the selected provider's status, presets and diff.
func renderDetail(m *app.Model, width, height int) string {
	p := m.Selected()
	if p == nil {
		return styleMuted.Render("no provider selected")
	}

	var b strings.Builder

	// Title row.
	st := m.Status()
	title := styleHead.Render(p.Title())
	badge := statusBadge(st)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Center, title, strings.Repeat(" ", maxInt(1, width-20)), badge))
	b.WriteString("\n")

	// Current value.
	if st.Current != "" {
		b.WriteString(styleLabel.Render("  当前  ") + styleValue.Render(truncate(st.Current, width-14)))
		b.WriteString("\n")
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
			s = s.Foreground(colorAccent)
		}
		b.WriteString(s.Render("  " + marker + " " + truncate(pr.Label, width-10)))
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
			b.WriteString("  " + renderChange(c, width-4))
			b.WriteString("\n")
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

// renderChange renders one planned change as a coloured +/- line.
func renderChange(c ChangeView, width int) string {
	switch c.Kind {
	case "env":
		return styleLabel.Render("env ") + c.Name +
			"  " + styleDel.Render(c.Before) + " → " + styleAdd.Render(c.After)
	case "env-unset":
		return styleLabel.Render("env ") + c.Name +
			"  " + styleDel.Render(c.Before) + " → " + styleMuted.Render("<移除>")
	default:
		file := c.File
		return styleAdd.Render("+ ") + truncate(file, width-6)
	}
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

// renderBar draws the status line.
func renderBar(m *app.Model, width int) string {
	left := ""
	if m.StatusMsg != "" {
		s := m.StatusMsg
		if m.StatusErr {
			left = styleErr.Render(s)
		} else {
			left = styleOK.Render(s)
		}
	} else if p := m.Selected(); p != nil {
		if m.Plan != nil && !m.Plan.Empty() {
			left = fmt.Sprintf("%s · %d 处待改 · 备份 %s",
				m.PresetLabel(p), m.Plan.ChangeCount(), lastBackup(m))
		} else {
			left = fmt.Sprintf("%s · 无需更改", m.PresetLabel(p))
		}
	}
	hint := styleMuted.Render("↑↓ 选择 · ←→ 方案集 · Enter 应用 · t 测速 · Ctrl+Z 撤销 · ? 帮助 · q 退出")
	bar := lipgloss.JoinHorizontal(lipgloss.Center, left, strings.Repeat(" ", maxInt(1, width-lipgloss.Width(left)-lipgloss.Width(hint))), hint)
	return styleBar.Width(width).Render(bar)
}

func lastBackup(m *app.Model) string {
	if m.LastBackup == nil {
		return "—"
	}
	return m.LastBackup.ID
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
