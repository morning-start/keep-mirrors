// Package env reads and writes user-level environment variables.
//
// There are two distinct jobs and it is worth keeping them apart:
//
//   - reading, which always works and is cheap;
//   - persisting, which is platform specific and has side effects beyond the
//     file we write.
//
// On Windows a persistent change means writing HKCU\Environment and
// broadcasting WM_SETTINGCHANGE so newly spawned processes pick it up. On
// POSIX it means appending an export line to the user's shell rc file. In
// both cases the value only reaches *currently running* processes if we
// set it in our own process, so Persist also touches our own environment.
//
// System-wide variables (/etc/environment, machine registry) are out of
// scope: a mirror manager must never require elevation.
package env

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"keep-mirrors/internal/paths"
)

// Get returns the value of name in the current process environment.
func Get(name string) string { return os.Getenv(name) }

// Set updates the current process environment only.
func Set(name, value string) error { return os.Setenv(name, value) }

// Unset removes name from the current process environment only.
func Unset(name string) error { return os.Unsetenv(name) }

// Snapshot captures a set of variables for later restoration.
type Snapshot map[string]string

// Take records the current values of names.
func Take(names ...string) Snapshot {
	s := make(Snapshot, len(names))
	for _, n := range names {
		s[n] = os.Getenv(n)
	}
	return s
}

// Restore puts the captured values back into the current process.
func (s Snapshot) Restore() {
	for n, v := range s {
		if v == "" {
			_ = os.Unsetenv(n)
			continue
		}
		_ = os.Setenv(n, v)
	}
}

// ShellKind identifies the shell whose rc file we edit.
type ShellKind string

const (
	ShellUnknown ShellKind = ""
	ShellBash    ShellKind = "bash"
	ShellZsh     ShellKind = "zsh"
	ShellFish    ShellKind = "fish"
	ShellSh      ShellKind = "sh"
	ShellCmd     ShellKind = "cmd"
	ShellPwsh    ShellKind = "powershell"
)

// DetectShell guesses the interactive shell from the environment.
func DetectShell() ShellKind {
	if runtime.GOOS == "windows" {
		// Prefer PowerShell when it is what launched us.
		if os.Getenv("PSModulePath") != "" {
			return ShellPwsh
		}
		return ShellCmd
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		return ShellUnknown
	}
	base := strings.ToLower(filepath.Base(sh))
	switch {
	case strings.Contains(base, "zsh"):
		return ShellZsh
	case strings.Contains(base, "fish"):
		return ShellFish
	case strings.Contains(base, "bash"):
		return ShellBash
	case strings.HasSuffix(base, "sh"):
		return ShellSh
	}
	return ShellUnknown
}

// ProfilePath returns the rc file edited for the given shell, or "" when we
// do not know where to write.
func ProfilePath(sh ShellKind) string {
	switch sh {
	case ShellBash:
		return filepath.Join(homeDir(), ".bashrc")
	case ShellZsh:
		return filepath.Join(homeDir(), ".zshrc")
	case ShellFish:
		return filepath.Join(homeDir(), ".config", "fish", "config.fish")
	case ShellPwsh:
		// $PROFILE is set inside a PowerShell session; fall back to the
		// conventional per-user path.
		if p := os.Getenv("PROFILE"); p != "" {
			return p
		}
		return filepath.Join(homeDir(), "Documents", "WindowsPowerShell",
			"Microsoft.PowerShell_profile.ps1")
	}
	return ""
}

// homeDir is resolved lazily so tests can override HOME.
//
// It goes through paths.Home rather than os.UserHomeDir so a redirected HOME is
// honoured everywhere, which is what makes profile editing testable.
func homeDir() string { return paths.Home() }

// Managed is the set of variables keep-mirrors writes, and the comment marker
// used to find its own block in a profile file.
const managedMarker = "keep-mirrors"

// ShellExport renders one variable as shell source text.
func ShellExport(sh ShellKind, name, value string) string {
	switch sh {
	case ShellFish:
		return fmt.Sprintf("set -gx %s %q", name, value)
	case ShellPwsh:
		return fmt.Sprintf("$env:%s = %q", name, value)
	default:
		return fmt.Sprintf("export %s=%q", name, value)
	}
}

// ShellUnset renders the removal of one variable as shell source text.
func ShellUnset(sh ShellKind, name string) string {
	switch sh {
	case ShellFish:
		return "set -e " + name
	case ShellPwsh:
		return fmt.Sprintf("Remove-Item Env:%s -ErrorAction SilentlyContinue", name)
	default:
		return "unset " + name
	}
}

// ShellComment renders a comment line in the given shell's syntax.
func ShellComment(sh ShellKind, text string) string {
	if sh == ShellCmd {
		return ":: " + text
	}
	return "# " + text
}

// SortNames returns names in stable order, for deterministic output.
func SortNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
