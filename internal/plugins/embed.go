// Package plugins embeds the curated plugin snapshot that ships with the
// binary.
//
// This is a seed, not a runtime source. km reads plugins from
// <appdata>/keep-mirrors/plugins/{official,user}/ at runtime; the copy here is
// unpacked into official/ once, so a fresh install works offline. After that it
// is never consulted again, which is what lets a registry sync or a user edit
// change behaviour without rebuilding the binary.
//
// The .lua files live in this package rather than at the repository root
// because go:embed cannot reach a parent directory, and having exactly one copy
// on disk removes the "edited the source, forgot to sync the copy" failure mode
// entirely.
package plugins

import (
	"embed"
	"fmt"
	"strconv"
	"strings"
)

// FS holds every embedded file: the plugins and their provenance record.
//
//go:embed *.lua PROVENANCE
var FS embed.FS

// Provenance records which commit the embedded snapshot was taken from.
//
// It exists because a binary built today may be run months later, and the first
// thing plugin sync needs to know is whether the snapshot it already carries is
// current. Without this, a fresh install would either re-download files it
// already has, or — worse — have no way to say which version of the plugin set
// a bug report came from.
//
// The SHA points at a commit on the plugins branch, not at the commit this
// binary was built from: the content came from there, and that is the identity
// worth comparing against.
type Provenance struct {
	// SHA is the plugins-branch commit the .lua files were taken from.
	SHA string
	// Source is the repository URL, for diagnostics.
	Source string
	// Branch is the branch the SHA belongs to.
	Branch string
}

// Valid reports whether the record carries a usable commit hash.
//
// A provenance file with an empty or malformed SHA is treated as absent rather
// than as an error: an unstamped snapshot still works, it just cannot answer
// "am I up to date" and will sync on first use.
func (p Provenance) Valid() bool { return validSHA(p.SHA) }

// Short returns the abbreviated SHA, or "unstamped".
func (p Provenance) Short() string {
	if !p.Valid() {
		return "unstamped"
	}
	return p.SHA[:7]
}

// Snapshot returns the embedded provenance record.
//
// It never fails: a missing or malformed PROVENANCE yields the zero value, and
// callers check Valid(). Making this a hard error would mean a release could be
// built but not run.
func Snapshot() Provenance {
	b, err := FS.ReadFile("PROVENANCE")
	if err != nil {
		return Provenance{}
	}
	return parseProvenance(string(b))
}

// parseProvenance reads the key=value format.
//
// key=value rather than a bare hash so the record can grow (a date, a checksum)
// without a format break, and so an unreadable line is simply ignored instead of
// poisoning the whole file.
//
// A leading UTF-8 BOM is stripped: editors and PowerShell both like to add one,
// and without this the first key becomes "\ufeffsha", which silently matches
// nothing and leaves the binary looking unstamped.
func parseProvenance(body string) Provenance {
	body = strings.TrimPrefix(body, "\ufeff")
	var p Provenance
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		switch strings.TrimSpace(k) {
		case "sha":
			p.SHA = strings.ToLower(v)
		case "source":
			p.Source = v
		case "branch":
			p.Branch = v
		}
	}
	return p
}

// validSHA reports whether s is a full lowercase hex git object name.
func validSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	if _, err := strconv.ParseUint(s[:16], 16, 64); err != nil {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// FormatProvenance renders a provenance record, for the stamp command.
func FormatProvenance(p Provenance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "sha=%s\n", p.SHA)
	if p.Source != "" {
		fmt.Fprintf(&b, "source=%s\n", p.Source)
	}
	if p.Branch != "" {
		fmt.Fprintf(&b, "branch=%s\n", p.Branch)
	}
	return b.String()
}
