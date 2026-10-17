package main

import (
	"os"
	"path/filepath"
	"testing"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/core"
	"keep-mirrors/internal/engine"
	"keep-mirrors/internal/env"
	"keep-mirrors/internal/paths"
	"keep-mirrors/internal/registry"
)

// sandbox points every path the tool resolves at t's temporary directory.
// Without this, a test would read and write the developer's real dotfiles and
// registry, which is both destructive and non-deterministic.
//
// CARGO_HOME and GOENV are cleared rather than redirected: both are honoured
// only when they point at an existing directory, so clearing them makes the
// plugins fall back to ~/.cargo and ~/.config/go inside the sandbox.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("CARGO_HOME", "")
	t.Setenv("GOENV", "")
	t.Setenv("GOPROXY", "")
	paths.Reset()
	t.Cleanup(paths.Reset)
	return home
}

// TestStatusReadBack is an end-to-end check that a provider can see what was
// applied. It is the regression test for a class of bug where the plan is
// correct, the write succeeds, and the status still reports "unconfigured"
// because the read path looks in the wrong place or parses the block wrongly.
func TestStatusReadBack(t *testing.T) {
	home := sandbox(t)

	engine.BuiltinFS = builtinPlugins
	reg, diag, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	reportBroken(diag)

	m := app.New(reg)
	m.Cursor = 0

	for _, id := range []string{"pip", "npm", "cargo", "go"} {
		p, ok := reg.Get(id)
		if !ok {
			t.Fatalf("provider %s not found", id)
		}
		m.Cursor = indexOf(reg, id)

		before := p.Status(m.Ctx())
		if before.Current != "" {
			t.Logf("%s: already configured as %q before the test", id, before.Current)
			continue
		}

		opts := presetOpts(p, "")
		if len(opts) == 0 {
			t.Errorf("%s: presetOpts returned no options; presets=%v", id, p.Presets())
			continue
		}
		plan, err := p.Plan(m.Ctx(), opts)
		if err != nil {
			t.Fatalf("%s: Plan: %v", id, err)
		}
		if err := plan.Describe(); err != nil {
			t.Fatalf("%s: Describe: %v", id, err)
		}
		if plan.Empty() {
			t.Fatalf("%s: empty plan for the default preset", id)
		}

		if _, err := core.Apply(plan); err != nil {
			t.Fatalf("%s: Apply: %v", id, err)
		}

		after := p.Status(m.Ctx())
		if after.Current == "" {
			t.Errorf("%s: status is still empty after apply", id)
			for _, f := range plan.Files() {
				body, _ := os.ReadFile(f)
				t.Logf("  %s:\n%s", f, body)
			}
			continue
		}
		t.Logf("%s: %q -> %q", id, before.Current, after.Current)
	}
	_ = home
}

// TestUndoRestoresEnv checks that an environment variable written by an apply
// is put back to its pre-apply value by the undo, and that the value survives
// a round trip through the backup manifest.
//
// The Windows registry is a machine-wide store with no per-process override,
// so this test cannot redirect it the way HOME is redirected. It therefore
// asserts the contract rather than a specific end state: whatever the variable
// was before the apply, that is what undo must restore.
func TestUndoRestoresEnv(t *testing.T) {
	sandbox(t)

	engine.BuiltinFS = builtinPlugins
	reg, diag, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	reportBroken(diag)

	m := app.New(reg)
	p, ok := reg.Get("npm")
	if !ok {
		t.Fatal("npm provider missing")
	}
	m.Cursor = indexOf(reg, "npm")

	// Capture the pre-apply value: it is what undo must restore, whatever it
	// happens to be on this machine.
	before, err := env.Read("npm_config_registry")
	if err != nil {
		t.Fatalf("env.Read: %v", err)
	}

	plan, err := p.Plan(m.Ctx(), presetOpts(p, "huawei"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := plan.Describe(); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	bak, err := core.Apply(plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if bak == nil {
		t.Fatal("Apply returned no backup")
	}

	after, err := env.Read("npm_config_registry")
	if err != nil {
		t.Fatalf("env.Read after apply: %v", err)
	}
	if after == before {
		// The huawei preset is deliberately not the one this machine already
		// had applied, so an unchanged value means the apply did nothing.
		t.Errorf("apply did not change npm_config_registry (still %q)", after)
	}

	// A backup read back from disk must still carry the env values, which is
	// what makes `km undo` work from a different process.
	reloaded, err := core.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	var found bool
	for _, b := range reloaded {
		if b.ID != bak.ID {
			continue
		}
		found = true
		if _, ok := b.EnvBefore["npm_config_registry"]; !ok {
			t.Error("reloaded backup lost npm_config_registry")
		}
	}
	if !found {
		t.Fatal("backup not found in ListBackups")
	}

	if err := core.Undo(bak); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	restored, err := env.Read("npm_config_registry")
	if err != nil {
		t.Fatalf("env.Read after undo: %v", err)
	}
	if restored != before {
		t.Errorf("npm_config_registry = %q after undo, want %q", restored, before)
	}
	if _, err := os.Stat(filepath.Join(paths.Home(), ".npmrc")); !os.IsNotExist(err) {
		t.Error(".npmrc still exists after undo")
	}
}

func indexOf(reg *registry.Registry, id string) int {
	for i, p := range reg.All() {
		if p.ID() == id {
			return i
		}
	}
	return 0
}
