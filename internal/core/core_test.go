package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"keep-mirrors/internal/config"
	"keep-mirrors/internal/marker"
	"keep-mirrors/internal/paths"
)

// sandbox points paths at a temp dir so a test never touches the real home.
//
// Reset is registered before the Setenv calls because t.Cleanup runs LIFO:
// registered last it would re-read the environment while it still pointed at
// the temp dir, leaving the package cached on a home that no longer exists.
func sandbox(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Cleanup(paths.Reset)
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	paths.OverrideHome(h, filepath.Join(h, ".config"))
	return h
}

func configPlan(t *testing.T, file, block string, entries ...config.Entry) *Plan {
	t.Helper()
	b := NewBuilder("test", "p1")
	b.SetFile(file, marker.Hash, config.FormatINI, block, entries...)
	return b.Result()
}

// TestDescribeDropsUnchangedChanges is the regression test for a plan that is
// already in effect still reporting work. Without the filter, every `km plan`
// on a configured mirror prints a diff of identical content, and the UI claims
// "2 处待改" when nothing would change.
func TestDescribeDropsUnchangedChanges(t *testing.T) {
	home := sandbox(t)
	file := filepath.Join(home, "test.conf")

	entries := []config.Entry{{Section: "global", Key: "index-url", Value: "https://a.invalid"}}

	// First pass: the file does not exist, so the change is real.
	p1 := configPlan(t, file, "test", entries...)
	if err := p1.Describe(); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if p1.Empty() {
		t.Fatal("first plan is empty; the block has not been written yet")
	}
	if _, err := Apply(p1); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Second pass: the same options must now be a no-op.
	p2 := configPlan(t, file, "test", entries...)
	if err := p2.Describe(); err != nil {
		t.Fatalf("Describe after apply: %v", err)
	}
	if !p2.Empty() {
		t.Fatalf("plan is not empty after applying the same block:\n%s", p2.Summary())
	}
}

// TestDescribeKeepsRealChanges guards the other direction: the filter must not
// swallow a change that actually differs.
func TestDescribeKeepsRealChanges(t *testing.T) {
	home := sandbox(t)
	file := filepath.Join(home, "test.conf")

	p1 := configPlan(t, file, "test",
		config.Entry{Section: "global", Key: "index-url", Value: "https://a.invalid"})
	if err := p1.Describe(); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if _, err := Apply(p1); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	p2 := configPlan(t, file, "test",
		config.Entry{Section: "global", Key: "index-url", Value: "https://b.invalid"})
	if err := p2.Describe(); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if p2.Empty() {
		t.Fatal("a different value produced an empty plan")
	}
	if _, err := Apply(p2); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); !strings.Contains(got, "https://b.invalid") {
		t.Fatalf("file was not updated:\n%s", got)
	}
}

// TestApplyIsIdempotent checks that applying twice leaves identical bytes, so
// a repeated apply does not churn the file (which would defeat the diff and
// confuse any file watcher).
func TestApplyIsIdempotent(t *testing.T) {
	home := sandbox(t)
	file := filepath.Join(home, "test.conf")

	mk := func() *Plan {
		p := configPlan(t, file, "test",
			config.Entry{Section: "global", Key: "index-url", Value: "https://a.invalid"})
		if err := p.Describe(); err != nil {
			t.Fatalf("Describe: %v", err)
		}
		return p
	}

	if _, err := Apply(mk()); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	first, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	// The second plan is empty, so Apply must not write at all.
	p := mk()
	if !p.Empty() {
		t.Fatalf("second plan is not empty:\n%s", p.Summary())
	}
	bak, err := Apply(p)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if bak != nil {
		t.Error("an empty plan still produced a backup")
	}

	second, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("file changed on a no-op apply:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}
