package core

import (
	"fmt"
	"os"
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
//
// Changes that would not alter anything are dropped. Without that, a preset
// that is already in effect still yields a non-empty plan, which makes the UI
// claim there is work to do and makes apply rewrite identical bytes.
func (p *Plan) Describe() error {
	kept := p.Changes[:0]
	for i := range p.Changes {
		c := p.Changes[i]
		if err := c.describe(); err != nil {
			return err
		}
		if c.Unchanged() {
			continue
		}
		kept = append(kept, c)
	}
	p.Changes = kept
	return nil
}

// describe computes Before/After/Summary for one change against the current
// on-disk state.
func (c *Change) describe() error {
	switch c.Kind {
	case ChangeConfig:
		cur, err := fsutil.Read(c.File)
		if err != nil {
			return fmt.Errorf("read %s: %w", c.File, err)
		}
		c.Before = cur
		body := config.Render(c.CF, c.Entries)
		c.After = marker.Replace(cur, marker.Block{Name: c.Block, Format: c.Format, Body: body})
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
	return nil
}

// Unchanged reports whether applying c would leave the world exactly as it is.
func (c Change) Unchanged() bool { return c.Before == c.After }

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

// readEnv reads a variable the way the target platform persists it.
//
// The persisted store is consulted first, deliberately. Our own process
// environment is written by every apply (so the value takes effect without a
// new terminal), which means os.Getenv would report the value *we* just wrote
// as if the user already had it. The registry / profile is what a fresh shell
// would see, and that is the thing the diff is about.
func readEnv(name string) (string, error) {
	v, err := readPersistedEnv(name)
	if err != nil {
		return "", err
	}
	if v != "" {
		return v, nil
	}
	// Not persisted anywhere yet: a value exported in the current session is
	// still worth showing, because the plugin will overwrite it.
	return os.Getenv(name), nil
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

// blockSlice extracts the managed block from lines, or nil when there is no
// block to show. Diffing the whole untouched file would bury the change in
// context the user did not ask about.
func blockSlice(lines []string, c Change) []string {
	content := strings.Join(lines, "\n")
	start, end, ok := marker.Find(content, c.Block, c.Format)
	if !ok {
		return nil
	}
	return lines[start:end]
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
