package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"keep-mirrors/internal/config"
	"keep-mirrors/internal/fsutil"
	"keep-mirrors/internal/marker"
	"keep-mirrors/internal/paths"
)

// Describe fills in Before/After/Summary for every change in the plan by
// reading current on-disk state. It must be called before rendering a diff,
// and again immediately before applying, so the diff cannot go stale relative
// to what actually gets written.
func (p *Plan) Describe() error {
	for i := range p.Changes {
		c := &p.Changes[i]
		switch c.Kind {
		case ChangeConfig:
			cur, err := fsutil.Read(c.File)
			if err != nil {
				return fmt.Errorf("read %s: %w", c.File, err)
			}
			c.Before = cur
			body := config.Render(c.CF, c.Entries)
			blk := marker.Block{Name: c.Block, Format: c.Format, Body: body}
			c.After = marker.Replace(cur, blk)
			c.Summary = describeConfig(c, body)

		case ChangeEnv:
			cur, err := readEnv(c.Name)
			if err != nil {
				return err
			}
			c.Before = cur
			c.After = c.Value
			c.Summary = fmt.Sprintf("env %s: %s -> %s", c.Name, orUnset(c.Before), c.Value)

		case ChangeEnvUnset:
			cur, err := readEnv(c.Name)
			if err != nil {
				return err
			}
			c.Before = cur
			c.After = ""
			c.Summary = fmt.Sprintf("env %s: remove (%s)", c.Name, orUnset(c.Before))
		}
	}
	return nil
}

func orUnset(v string) string {
	if v == "" {
		return "<unset>"
	}
	return v
}

func describeConfig(c *Change, body string) string {
	file := displayPath(c.File)
	if marker.Present(c.Before, marker.Block{Name: c.Block, Format: c.Format, Body: body}) {
		return fmt.Sprintf("%s: already configured", file)
	}
	if _, _, ok := marker.Find(c.Before, c.Block, c.Format); ok {
		return fmt.Sprintf("%s: update block %q", file, c.Block)
	}
	return fmt.Sprintf("%s: add block %q", file, c.Block)
}

// displayPath shortens the home prefix to "~" for readable diffs.
func displayPath(p string) string {
	h := paths.Home()
	if h != "" && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

// readEnv reads a variable the way the target platform stores it, so the
// diff compares persisted state rather than this process's environment.
func readEnv(name string) (string, error) {
	if v := os.Getenv(name); v != "" {
		return v, nil
	}
	// Fall back to the platform store so an unset-in-process but persisted
	// variable still shows up as a change rather than as "unset -> set".
	return readPersistedEnv(name)
}

// Diff renders a unified diff of Before/After for one change. It is a
// line-oriented comparison, not a real diff algorithm: managed blocks are
// small and the output is read by humans, so clarity beats minimality.
func Diff(c Change) string {
	before := strings.Split(strings.TrimRight(c.Before, "\n"), "\n")
	after := strings.Split(strings.TrimRight(c.After, "\n"), "\n")

	var b strings.Builder
	switch c.Kind {
	case ChangeConfig:
		bs := blockSlice(before, c)
		as := blockSlice(after, c)
		fmt.Fprintf(&b, "%s\n", displayPath(c.File))
		for _, l := range bs {
			b.WriteString("  - " + l + "\n")
		}
		for _, l := range as {
			b.WriteString("  + " + l + "\n")
		}
		if len(bs) == 0 && len(as) == 0 {
			b.WriteString("    (no change)\n")
		}

	case ChangeEnv, ChangeEnvUnset:
		fmt.Fprintf(&b, "env %s\n", c.Name)
		fmt.Fprintf(&b, "  - %s\n", orUnset(c.Before))
		fmt.Fprintf(&b, "  + %s\n", orUnset(c.After))
	}
	return strings.TrimRight(b.String(), "\n")
}

// blockSlice extracts the managed block (or the whole file when the block is
// absent) from lines.
func blockSlice(lines []string, c Change) []string {
	content := strings.Join(lines, "\n")
	body := config.Render(c.CF, c.Entries)
	blk := marker.Block{Name: c.Block, Format: c.Format, Body: body}
	if !marker.Present(content, blk) {
		if start, end, ok := marker.Find(content, c.Block, c.Format); ok {
			return lines[start:end]
		}
	}
	if start, end, ok := marker.Find(content, c.Block, c.Format); ok {
		return lines[start:end]
	}
	return nil
}

// SortChanges orders changes so that files are grouped by path, making the
// diff view and the backup manifest easier to read.
func (p *Plan) SortChanges() {
	sort.SliceStable(p.Changes, func(i, j int) bool {
		a, b := p.Changes[i], p.Changes[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Name < b.Name
	})
}

// Files returns the distinct files touched by the plan.
func (p *Plan) Files() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range p.Changes {
		if c.File == "" || seen[c.File] {
			continue
		}
		seen[c.File] = true
		out = append(out, c.File)
	}
	return out
}

// FileExists reports whether p exists, used by the UI to distinguish "will
// create" from "will modify".
func FileExists(p string) bool { return fsutil.Exists(p) }

// BaseName is a convenience for rendering file paths in the status bar.
func BaseName(p string) string { return filepath.Base(p) }
