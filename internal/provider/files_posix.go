//go:build !windows

package provider

import (
	"os"
	"path/filepath"

	"keep-mirrors/internal/paths"
)

// CandidateFiles returns the existing paths among cand, resolved against the
// home directory. Resolution goes through paths.Home rather than
// os.UserHomeDir so a redirected HOME is honoured.
func CandidateFiles(cand ...string) []string {
	h := paths.Home()
	if h == "" {
		return nil
	}
	var out []string
	for _, c := range cand {
		p := filepath.Join(h, c)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			out = append(out, p)
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
