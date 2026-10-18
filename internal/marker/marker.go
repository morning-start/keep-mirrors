// Package marker implements managed-block editing of configuration files.
//
// A "managed block" is a region delimited by sentinel comment lines that
// keep-mirrors owns:
//
//	# >>> keep-mirrors:pip >>>
//	[global]
//	index-url = https://pypi.tuna.tsinghua.edu.cn/simple
//	# <<< keep-mirrors:pip <<<
//
// Everything outside the block is left byte-for-byte identical, so user
// comments, hand-written settings and tool-specific formatting survive an
// apply. Rewriting a block is idempotent: the same options always produce the
// same file. Removing a block (Remove) strips the sentinels and the content
// between them, plus the separator line Replace added above the block, and
// leaves every other byte untouched.
//
// This is deliberately not a config parser. Parsing and re-serialising a
// .toml or .npmrc file loses information we cannot reconstruct (comment
// placement, key ordering, quote style), and a mirror manager has no business
// reformatting a user's dotfiles.
package marker

import (
	"fmt"
	"regexp"
	"strings"
)

// Format describes the comment syntax of the target file.
type Format int

const (
	// Hash uses "#" for both the sentinels and the inner comments (INI,
	// TOML, shell rc files, pip.conf).
	Hash Format = iota
	// Semicolon uses ";" (some INI dialects).
	Semicolon
	// Slash uses "//" (JSON-with-comments, some YAML tooling).
	Slash
	// XML uses "<!-- -->" (.npmrc-adjacent XML, plist).
	XML
)

// Comment renders a comment line in the given format.
func (f Format) Comment(text string) string {
	switch f {
	case Semicolon:
		return "; " + text
	case Slash:
		return "// " + text
	case XML:
		return "<!-- " + text + " -->"
	default:
		return "# " + text
	}
}

// Block is one managed region inside a file.
type Block struct {
	Name   string // logical name, e.g. "pip"
	Format Format
	Body   string // the managed content, without sentinels
}

// Head returns the opening sentinel line for b.
func (b Block) Head() string { return b.Format.Comment(fmt.Sprintf(">>> keep-mirrors:%s >>>", b.Name)) }

// Tail returns the closing sentinel line for b.
func (b Block) Tail() string { return b.Format.Comment(fmt.Sprintf("<<< keep-mirrors:%s <<<", b.Name)) }

var sentinelRe = regexp.MustCompile(`^[ \t]*[#;/]|^[ \t]*<!--`)

// Find locates the managed block called name in content. It returns the
// half-open line range [start,end) of the whole block including sentinels,
// and ok=false when the block is absent or malformed (a head without a tail
// is treated as absent so that a half-written block is replaced, not
// duplicated).
func Find(content, name string, f Format) (start, end int, ok bool) {
	lines := strings.Split(content, "\n")
	head := fmt.Sprintf(">>> keep-mirrors:%s >>>", name)
	tail := fmt.Sprintf("<<< keep-mirrors:%s <<<", name)

	start, end = -1, -1
	for i, ln := range lines {
		if start < 0 {
			if strings.Contains(ln, head) && sentinelRe.MatchString(ln) {
				start = i
			}
			continue
		}
		if strings.Contains(ln, tail) && sentinelRe.MatchString(ln) {
			end = i + 1
			return start, end, true
		}
	}
	return -1, -1, false
}

// Replace inserts or rewrites the managed block in content and returns the
// new content. The block is placed at the end of the file when it does not
// exist yet, which keeps it clear of any content the user may have at the top
// of the file (shebangs, license headers, tool preambles).
func Replace(content string, b Block) string {
	block := b.Head() + "\n" + strings.TrimRight(b.Body, "\n") + "\n" + b.Tail()

	if start, end, ok := Find(content, b.Name, b.Format); ok {
		lines := strings.Split(content, "\n")
		out := make([]string, 0, len(lines))
		out = append(out, lines[:start]...)
		out = append(out, block)
		out = append(out, lines[end:]...)
		return strings.Join(out, "\n")
	}

	if content == "" {
		return block + "\n"
	}
	sep := "\n"
	if !strings.HasSuffix(content, "\n") {
		sep = "\n\n"
	}
	return content + sep + block + "\n"
}

// Remove strips the managed block from content and returns the new content.
//
// The blank separator line Replace put above the block goes with it, so
// Replace followed by Remove reproduces the original bytes. Blank lines
// anywhere else are left exactly as they are: a run of blank lines far from
// the block is the user's formatting, and "tidying" it would rewrite parts of
// the file the block never touched. An earlier version collapsed every
// "\n\n\n" run in the whole file, which is why the tests pin the bytes on both
// sides of the block.
func Remove(content, name string, f Format) string {
	start, end, ok := Find(content, name, f)
	if !ok {
		return content
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines)-(end-start))
	out = append(out, lines[:start]...)
	// Drop one blank line directly above the head sentinel: that is the
	// separator Replace added, not the user's spacing.
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// Present reports whether content already carries a managed block with this
// name and exactly this body. It is how the TUI decides between "will change"
// and "already configured" without duplicating the diff logic.
func Present(content string, b Block) bool {
	start, end, ok := Find(content, b.Name, b.Format)
	if !ok {
		return false
	}
	lines := strings.Split(content, "\n")
	got := strings.Join(lines[start+1:end-1], "\n")
	return strings.TrimRight(got, "\n") == strings.TrimRight(b.Body, "\n")
}
