package tui

import (
	"strings"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/core"
	"keep-mirrors/internal/paths"
)

// ChangeView is a flattened, rendering-friendly form of core.Change. The
// view layer works from these so it never has to import the core types or
// know about formats.
type ChangeView struct {
	Kind   string
	File   string
	Name   string
	Before string
	After  string
	// Diff carries the line-oriented diff of a config change, as produced by
	// core.Diff: a file path line followed by "  - old" / "  + new" lines.
	// It is nil for env changes, which render as one compact line.
	Diff []string
}

// BenchView is a flattened benchmark row.
type BenchView struct {
	Name string
	URL  string
	MS   int64
	OK   bool
	Err  string
}

// changeViews converts a plan into view models.
func changeViews(p *core.Plan) []ChangeView {
	if p == nil {
		return nil
	}
	out := make([]ChangeView, 0, len(p.Changes))
	for _, c := range p.Changes {
		v := ChangeView{Kind: string(c.Kind)}
		switch c.Kind {
		case core.ChangeConfig, core.ChangeConfigRemove:
			v.File = shortPath(c.File)
			// The same diff the headless `km plan` prints. The preview pane
			// must show what will actually change — a bare file name made
			// the user approve writes they could not see.
			if d := core.Diff(c); d != "" {
				v.Diff = strings.Split(d, "\n")
			}
		case core.ChangeEnv, core.ChangeEnvUnset:
			v.Name = c.Name
			v.Before = orUnset(c.Before)
			v.After = c.After
		}
		out = append(out, v)
	}
	return out
}

// benchViews converts provider results into view models.
func benchViews(rs []app.BenchRow) []BenchView {
	out := make([]BenchView, 0, len(rs))
	for _, r := range rs {
		out = append(out, BenchView{Name: r.Name, MS: r.MS, OK: r.OK, Err: r.Err})
	}
	return out
}

// shortPath renders a path relative to home for display.
func shortPath(p string) string {
	h := paths.Home()
	if h != "" && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

func orUnset(s string) string {
	if s == "" {
		return "<unset>"
	}
	return s
}
