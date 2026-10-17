package env

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Profile editing keeps keep-mirrors' own exports inside a marked block and
// leaves every other line of the user's shell configuration untouched:
//
//	# >>> keep-mirrors:env >>>
//	export PIP_INDEX_URL="https://pypi.tuna.tsinghua.edu.cn/simple"
//	# <<< keep-mirrors:env <<<
//
// Parsing is line oriented and deliberately forgiving: a hand-edited or
// foreign export line is still recognised, because the point of ParseProfile
// is to answer "what does this profile currently set", not to validate it.

var (
	// bash/zsh: export NAME="value" | export NAME=value | NAME="value"
	exportRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
	// fish: set -gx NAME "value" | set NAME value
	fishRe = regexp.MustCompile(`^\s*set\s+(?:-(?:gx|g|u|U)\s+)*([A-Za-z_][A-Za-z0-9_]*)\s+(.*)$`)
	// powershell: $env:NAME = "value"
	pwshRe = regexp.MustCompile(`^\s*\$env:([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$`)
)

const (
	blockHead = ">>> keep-mirrors:env >>>"
	blockTail = "<<< keep-mirrors:env <<<"
)

// ParseProfile extracts the variables a profile exports. Values outside the
// managed block are reported too, so status functions can tell "the user
// already exports this by hand" from "keep-mirrors set it".
func ParseProfile(body string, sh ShellKind) map[string]string {
	out := map[string]string{}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == ShellComment(sh, blockHead) || line == ShellComment(sh, blockTail) {
			continue
		}
		name, value, ok := parseLine(line, sh)
		if !ok {
			continue
		}
		out[name] = value
	}
	return out
}

func parseLine(line string, sh ShellKind) (string, string, bool) {
	switch sh {
	case ShellFish:
		return match(fishRe, line)
	case ShellPwsh:
		return match(pwshRe, line)
	default:
		return match(exportRe, line)
	}
}

func match(re *regexp.Regexp, line string) (string, string, bool) {
	m := re.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return m[1], unquoteValue(strings.TrimSpace(m[2])), true
}

func unquoteValue(v string) string {
	// Drop trailing comments that follow an unquoted value.
	if !strings.HasPrefix(v, `"`) && !strings.HasPrefix(v, "'") {
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
	}
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// UpsertProfile applies set and unset to body and returns the result.
//
// set replaces an existing entry (inside or outside the block) or appends one;
// unset removes entries wherever they are. Because foreign lines are rewritten
// in place, a user who hand-maintains PIP_INDEX_URL keeps it in the position
// they chose, and we never duplicate the variable.
func UpsertProfile(body string, sh ShellKind, set map[string]string, unset []string) string {
	lines := strings.Split(body, "\n")
	unsetSet := map[string]bool{}
	for _, n := range unset {
		unsetSet[n] = true
	}

	kept := make([]string, 0, len(lines)+len(set))
	inBlock := false
	blockIdx := -1

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == ShellComment(sh, blockHead) {
			inBlock = true
			kept = append(kept, raw)
			blockIdx = len(kept) - 1
			continue
		}
		if line == ShellComment(sh, blockTail) {
			inBlock = false
			kept = append(kept, raw)
			continue
		}
		if inBlock {
			// Block content is always regenerated below.
			continue
		}
		if name, _, ok := parseLine(line, sh); ok {
			if unsetSet[name] {
				continue // drop it
			}
			if v, ok := set[name]; ok {
				kept = append(kept, ShellExport(sh, name, v))
				delete(set, name)
				continue
			}
		}
		kept = append(kept, raw)
	}

	// Anything left in set has no home yet: rebuild the managed block.
	block := renderBlock(sh, set)
	if len(block) == 0 {
		// No new entries: keep the file as-is minus any stale block body.
		if blockIdx >= 0 {
			return strings.Join(stripBlock(kept, sh), "\n")
		}
		return strings.Join(kept, "\n")
	}

	if blockIdx >= 0 {
		out := make([]string, 0, len(kept)+len(block))
		out = append(out, kept[:blockIdx+1]...)
		out = append(out, block...)
		out = append(out, kept[blockIdx+1:]...)
		return strings.Join(out, "\n")
	}
	return strings.Join(append(kept, block...), "\n")
}

// renderBlock builds the managed block lines for set.
func renderBlock(sh ShellKind, set map[string]string) []string {
	if len(set) == 0 {
		return nil
	}
	out := []string{ShellComment(sh, blockHead)}
	for _, n := range sortedKeys(set) {
		out = append(out, ShellExport(sh, n, set[n]))
	}
	return append(out, ShellComment(sh, blockTail))
}

// stripBlock removes a managed block that has become empty, including the
// blank line it leaves behind, so repeated apply/remove cycles converge.
func stripBlock(lines []string, sh ShellKind) []string {
	out := make([]string, 0, len(lines))
	skip := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == ShellComment(sh, blockHead) {
			skip = true
			continue
		}
		if t == ShellComment(sh, blockTail) {
			skip = false
			continue
		}
		if skip {
			continue
		}
		out = append(out, l)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	return out
}

// WriteProfile writes body to path, creating parents as needed.
func WriteProfile(path, body string) error {
	d := dirOf(path)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", d, err)
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// dirOf returns the directory part of a slash-separated path.
func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "."
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
