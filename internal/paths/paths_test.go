package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withHome points the package at h for the duration of the test.
func withHome(t *testing.T, h string) {
	t.Helper()
	old := home
	oldCfg := cfg
	home = h
	cfg = filepath.Join(h, ".config")
	t.Cleanup(func() {
		home = old
		cfg = oldCfg
	})
}

// TestExpandKeepsDriveLetters is the regression test for filepath.Join eating
// the colon in "C:\x", which silently turned an absolute path into one under
// home and defeated every containment check built on it.
func TestExpandKeepsDriveLetters(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("windows-only path shape")
	}
	h := t.TempDir()
	withHome(t, h)

	got := Expand(`C:\Windows\System32`)
	if strings.HasPrefix(got, h) {
		t.Errorf("Expand(%q) = %q, which was joined onto home", `C:\Windows\System32`, got)
	}
	if filepath.VolumeName(got) != "C:" {
		t.Errorf("Expand(%q) = %q, lost its volume name", `C:\Windows\System32`, got)
	}
}

// TestExpandTilde checks the forms a plugin actually writes.
func TestExpandTilde(t *testing.T) {
	h := t.TempDir()
	withHome(t, h)

	for _, in := range []string{"~", "~/x", `~\x`} {
		got := Expand(in)
		if !strings.HasPrefix(got, h) {
			t.Errorf("Expand(%q) = %q, want it under %q", in, got, h)
		}
	}
	if got := Expand("relative.txt"); got != filepath.Join(h, "relative.txt") {
		t.Errorf("Expand(relative) = %q", got)
	}
}

// TestWithinHomeRejectsEscapes covers the lexical boundary.
func TestWithinHomeRejectsEscapes(t *testing.T) {
	h := t.TempDir()
	withHome(t, h)

	if !WithinHome("~/inside.txt") {
		t.Error("a file directly under home was rejected")
	}
	if WithinHome("~/../../etc/passwd") {
		t.Error("a ../ escape was accepted")
	}
}

// TestWithinHomeResolvedRejectsSymlinkEscape is the security test for the
// resolved check: a link inside home pointing outside it passes the lexical
// check, because the path really does start with home.
//
// Creating a symlink needs a privilege Windows does not grant by default, so
// the test skips there; the unix behaviour is what the check exists for.
func TestWithinHomeResolvedRejectsSymlinkEscape(t *testing.T) {
	h := t.TempDir()
	outside := t.TempDir()
	withHome(t, h)

	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(h, "escape.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	// The lexical check is fooled; that is exactly why the resolved one exists.
	if !WithinHome(link) {
		t.Log("lexical check already rejected the link (unusual but fine)")
	}
	if WithinHomeResolved(link) {
		t.Error("a symlink pointing outside home was accepted by WithinHomeResolved")
	}
}

// TestWithinHomeResolvedAllowsMissingPath checks that a file which does not
// exist yet is still judged by where it would land, rather than rejected
// outright because EvalSymlinks could not resolve it.
func TestWithinHomeResolvedAllowsMissingPath(t *testing.T) {
	h := t.TempDir()
	withHome(t, h)

	if !WithinHomeResolved(filepath.Join(h, "not", "created", "yet.conf")) {
		t.Error("a not-yet-existing file inside home was rejected")
	}
}

// TestWithinHomeResolvedTerminates checks that the parent walk used to resolve a
// missing path always terminates.
//
// An infinite loop in a containment check is worse than the escape it guards
// against: it would hang every km.fs call. The awkward inputs below are the ones
// where filepath.Dir stops making progress — a bare drive-relative path and a
// volume root — so the walk has to notice it cannot climb any higher.
func TestWithinHomeResolvedTerminates(t *testing.T) {
	h := t.TempDir()
	withHome(t, h)

	inputs := []string{"C:foo", "C:", `C:\`, "/", ".", "..", "~/..", "~/../../.."}
	if os.PathSeparator == '\\' {
		inputs = append(inputs, `\\?\C:\x`, `\\.\PhysicalDrive0`)
	}
	for _, in := range inputs {
		done := make(chan bool, 1)
		go func(p string) { done <- WithinHomeResolved(p) }(in)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("WithinHomeResolved(%q) did not terminate", in)
		}
	}
}
