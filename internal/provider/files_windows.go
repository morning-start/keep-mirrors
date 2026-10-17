//go:build windows

package provider

import (
	"os"
	"path/filepath"

	"keep-mirrors/internal/paths"
)

// CandidateFiles returns the existing paths among cand, resolved against the
// home directory and the usual Windows config roots.
//
// Resolution goes through paths.Home rather than os.UserHomeDir so that a test
// (or a future sandboxed run) pointing HOME elsewhere is honoured.
func CandidateFiles(cand ...string) []string {
	roots := []string{paths.Home()}
	if a := os.Getenv("APPDATA"); a != "" && a != paths.Home() {
		roots = append(roots, a)
	}
	var out []string
	for _, c := range cand {
		for _, r := range roots {
			if r == "" {
				continue
			}
			p := filepath.Join(r, c)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// FirstExisting returns the first existing candidate, or the first candidate
// unchanged so callers can still write there.
func FirstExisting(cand ...string) string {
	if got := CandidateFiles(cand...); len(got) > 0 {
		return got[0]
	}
	return filepath.Join(paths.Home(), cand[0])
}
