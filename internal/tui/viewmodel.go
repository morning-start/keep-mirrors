package tui

import (
	"keep-mirrors/internal/app"
	"keep-mirrors/internal/core"
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
		case core.ChangeConfig:
			v.File = shortPath(c.File)
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
	h := homeDir()
	if h != "" && len(p) > len(h) && p[:len(h)] == h {
		return "~" + p[len(h):]
	}
	return p
}

func orUnset(s string) string {
	if s == "" {
		return "<unset>"
	}
	return s
}
