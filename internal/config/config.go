// Package config renders and parses the small subset of configuration syntax
// that mirror settings actually need, and nothing more.
//
// The subset is uniform across the formats we care about: a flat sequence of
// "section, key, value" triples. INI, TOML and npmrc all reduce to it, and
// keeping one renderer means pip, cargo and npm providers stay declarative
// instead of each hand-rolling string concatenation.
//
// Values are always written into a keep-mirrors managed block (see the marker
// package), so this package never has to preserve foreign formatting: it only
// ever produces the block body, and only ever reads back a block it produced.
package config

import (
	"strconv"
	"strings"
)

// Format selects the syntax used to render a body.
type Format string

const (
	// FormatINI is pip.conf / setup.cfg style: "key = value", unquoted.
	FormatINI Format = "ini"
	// FormatTOML is cargo config style: `key = "value"`, quoted.
	FormatTOML Format = "toml"
	// FormatKV is .npmrc / .gitconfig-ish style: "key=value", no sections.
	FormatKV Format = "kv"
	// FormatYAML is "key: value" under an optional top-level section.
	FormatYAML Format = "yaml"
)

// Entry is one setting inside a managed block.
type Entry struct {
	// Section is the INI section or TOML table. Empty means "no section",
	// which is what .npmrc and top-level YAML documents use.
	Section string
	Key     string
	Value   string
	// Comment is an optional trailing comment rendered on the same line.
	Comment string
}

// Render produces the body of a managed block for entries in the given
// format. Consecutive entries sharing a section are emitted under a single
// section header.
func Render(format Format, entries []Entry) string {
	var b strings.Builder
	cur := "\x00" // sentinel: no section emitted yet
	first := true

	emitSection := func(name string) {
		if name == "" {
			return
		}
		if !first {
			b.WriteString("\n")
		}
		switch format {
		case FormatTOML, FormatINI:
			b.WriteString("[" + name + "]\n")
		case FormatYAML:
			b.WriteString(name + ":\n")
		}
	}

	for _, e := range entries {
		if e.Section != cur {
			emitSection(e.Section)
			cur = e.Section
		}
		first = false
		switch format {
		case FormatKV:
			b.WriteString(e.Key + "=" + e.Value + "\n")
		case FormatYAML:
			b.WriteString("  " + e.Key + ": " + quoteYAML(e.Value) + "\n")
		case FormatTOML:
			// The renderer quotes the value itself, so a value that already
			// carries quotes is passed through untouched. This lets a plugin
			// emit a literal TOML string (single-quoted, no escapes) or a
			// basic string, which cargo's config.toml uses for replace-with.
			if isTOMLQuoted(e.Value) {
				b.WriteString(e.Key + " = " + e.Value + "\n")
			} else {
				b.WriteString(e.Key + " = " + quoteTOML(e.Value) + "\n")
			}
		default: // FormatINI
			b.WriteString(e.Key + " = " + e.Value + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func quoteTOML(v string) string {
	if v == "" {
		return `""`
	}
	return strconv.Quote(v)
}

// isTOMLQuoted reports whether v already carries TOML string delimiters.
// Cargo's replace-with uses a single-quoted literal string, so both forms
// have to be recognised.
func isTOMLQuoted(v string) bool {
	if len(v) < 2 {
		return false
	}
	return (v[0] == '"' && v[len(v)-1] == '"') ||
		(v[0] == '\'' && v[len(v)-1] == '\'')
}

func quoteYAML(v string) string {
	if v == "" {
		return `""`
	}
	// Bare when it is unambiguously scalar, quoted otherwise.
	if strings.ContainsAny(v, ":#{}[]&*!|>'\"%@`,") || strings.HasPrefix(v, " ") {
		return strconv.Quote(v)
	}
	return v
}

// Parse reads back entries from a body produced by Render. Unknown lines are
// skipped rather than rejected: a user may have edited the block by hand and
// status reporting should degrade, not fail.
func Parse(format Format, body string) []Entry {
	var out []Entry
	section := ""
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if format == FormatYAML && !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":") {
			section = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		k, v, ok := splitKV(line, format)
		if !ok {
			continue
		}
		out = append(out, Entry{Section: section, Key: k, Value: v})
	}
	return out
}

func splitKV(line string, format Format) (string, string, bool) {
	sep := " = "
	switch format {
	case FormatKV:
		sep = "="
	case FormatYAML:
		sep = ": "
	}
	i := strings.Index(line, sep)
	if i < 0 {
		// Fall back to the loosest separator so hand-edited blocks still parse.
		i = strings.IndexAny(line, "=:")
		if i < 0 {
			return "", "", false
		}
	}
	k := strings.TrimSpace(line[:i])
	v := strings.TrimSpace(line[i+len(sep):])
	if k == "" {
		return "", "", false
	}
	v = unquote(v)
	return k, v, true
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// Get returns the value of section/key, or "" when absent. A nil-safe helper
// for provider status functions.
func Get(entries []Entry, section, key string) string {
	for _, e := range entries {
		if e.Key == key && e.Section == section {
			return e.Value
		}
	}
	return ""
}
