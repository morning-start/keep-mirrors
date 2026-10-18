package env

import (
	"fmt"
	"os"
	"path/filepath"
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
//
// Entries already inside our block are the starting point of the rebuild, not
// something to discard: `km apply` writes one variable per call, so a function
// that rebuilt the block from `set` alone would make each write silently erase
// the variables written before it.
func UpsertProfile(body string, sh ShellKind, set map[string]string, unset []string) string {
	lines := strings.Split(body, "\n")
	unsetSet := map[string]bool{}
	for _, n := range unset {
		unsetSet[n] = true
	}

	kept := make([]string, 0, len(lines)+len(set)+3)
	blockVars := map[string]string{}
	foreign := map[string]bool{}
	blockAt := -1 // where the rebuilt block goes; -1 means "append"
	inBlock := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case line == ShellComment(sh, blockHead):
			// Every marker is consumed, including the duplicate heads an older
			// version of this function could leave behind: one pass has to be
			// enough to converge a damaged profile.
			if blockAt < 0 {
				blockAt = len(kept)
			}
			inBlock = true
			continue
		case line == ShellComment(sh, blockTail):
			inBlock = false
			continue
		case inBlock:
			if n, v, ok := parseLine(line, sh); ok {
				blockVars[n] = v
			}
			continue
		}

		if name, _, ok := parseLine(line, sh); ok {
			if unsetSet[name] {
				delete(blockVars, name)
				continue // drop it
			}
			if v, ok := set[name]; ok {
				kept = append(kept, ShellExport(sh, name, v))
				delete(set, name)
				// The variable now lives on this line; leaving it in the block
				// too would export it twice.
				delete(blockVars, name)
				continue
			}
			// A hand-written entry the caller is not touching. It owns the
			// variable, so the block's copy goes — one name, one line.
			foreign[name] = true
		}
		kept = append(kept, raw)
	}

	// Anything left in set has no home yet: rebuild the managed block around
	// the variables it already held.
	for n, v := range set {
		blockVars[n] = v
	}
	for n := range unsetSet {
		delete(blockVars, n)
	}
	for n := range foreign {
		delete(blockVars, n)
	}

	block := renderBlock(sh, blockVars)
	if len(block) == 0 {
		return strings.Join(kept, "\n")
	}
	if blockAt < 0 {
		blockAt = len(kept)
	}
	out := make([]string, 0, len(kept)+len(block))
	out = append(out, kept[:blockAt]...)
	out = append(out, block...)
	out = append(out, kept[blockAt:]...)
	return strings.Join(out, "\n")
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

// WriteProfile writes body to path, creating parents as needed.
func WriteProfile(path, body string) error {
	d := filepath.Dir(path)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", d, err)
	}
	return os.WriteFile(path, []byte(body), 0o644)
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
