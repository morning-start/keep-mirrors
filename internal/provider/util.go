package provider

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Which resolves a command name against PATH and the usual install roots,
// returning the full path or "" when it is not present. It is used for
// detection and for running a provider's own CLI (npm config get, cargo
// --version) without hardcoding install locations.
func Which(name string) string {
	if name == "" {
		return ""
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	// Windows sometimes misses the per-user npm prefix even though it is on
	// PATH; check the conventional locations directly.
	extra := []string{
		filepath.Join(os.Getenv("APPDATA"), "npm"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs"),
		filepath.Join(os.Getenv("USERPROFILE"), "scoop", "shims"),
		filepath.Join(os.Getenv("USERPROFILE"), ".cargo", "bin"),
	}
	for _, dir := range extra {
		if dir == "" {
			continue
		}
		for _, ext := range []string{".exe", ".cmd", ".bat", ""} {
			cand := filepath.Join(dir, name+ext)
			if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
				return cand
			}
		}
	}
	return ""
}

// Run executes a command with a timeout and returns its trimmed stdout.
// stderr is folded into the error only, so providers can parse stdout
// without contending with progress output.
func Run(name string, args ...string) (string, error) {
	return RunDir("", name, args...)
}

// RunDir is Run with an explicit working directory.
func RunDir(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()),
			fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err,
				strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Defaults builds the default opts map from a provider's settings.
func Defaults(s []Setting) map[string]any {
	m := make(map[string]any, len(s))
	for _, st := range s {
		if st.Default != nil {
			m[st.Key] = st.Default
		}
	}
	return m
}

// PresetByID returns the preset with the given id, or nil.
func PresetByID(ps []Preset, id string) *Preset {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

// PresetIDs lists preset ids in declaration order.
func PresetIDs(ps []Preset) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

// SortedIDs sorts ids for stable UI ordering.
func SortedIDs(m map[string]Provider) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MergeOpts overlays overrides onto base without mutating either.
func MergeOpts(base, overrides map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}
