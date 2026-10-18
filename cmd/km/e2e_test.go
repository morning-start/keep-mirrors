package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/core"
	"keep-mirrors/internal/engine"
	"keep-mirrors/internal/env"
	"keep-mirrors/internal/paths"
	"keep-mirrors/internal/provider"
	"keep-mirrors/internal/registry"
)

// sandbox points every path the tool resolves at t's temporary directory.
// Without this, a test would read and write the developer's real dotfiles and
// registry, which is both destructive and non-deterministic.
//
// CARGO_HOME and GOENV are cleared rather than redirected: both are honoured
// only when they point at an existing directory, so clearing them makes the
// plugins fall back to ~/.cargo and ~/.config/go inside the sandbox.
//
// APPDATA is redirected too, which now also moves the plugin directories:
// paths.AppDir is derived from it, so the loader seeds and reads plugins
// inside the temp tree instead of the developer's real ones.
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
	// The tests call loadRegistry directly instead of going through run(), so
	// the seed has to be installed here too. Doing it explicitly is what makes
	// the seeding path part of what these tests cover.
	engine.SetSeed(pluginSeed)
	return home
}

// TestStatusReadBack is an end-to-end check that a provider can see what was
// applied. It is the regression test for a class of bug where the plan is
// correct, the write succeeds, and the status still reports "unconfigured"
// because the read path looks in the wrong place or parses the block wrongly.
func TestStatusReadBack(t *testing.T) {
	home := sandbox(t)

	// loadRegistry goes through newLoader, which seeds the official directory
	// from the embedded snapshot when it is empty. Inside the sandbox that is
	// a fresh directory, so this also exercises the seed path end to end.
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

		opts, pid, err := resolvePreset(p, "")
		if err != nil {
			t.Fatalf("%s: resolvePreset: %v", id, err)
		}
		if len(opts) == 0 {
			t.Errorf("%s: resolvePreset returned no options; presets=%v", id, p.Presets())
			continue
		}
		plan, err := p.Plan(m.Ctx(), withPreset(opts, pid))
		if err != nil {
			t.Fatalf("%s: Plan: %v", id, err)
		}
		plan.PresetID = pid
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

	opts, pid, err := resolvePreset(p, "huawei")
	if err != nil {
		t.Fatalf("resolvePreset: %v", err)
	}
	plan, err := p.Plan(m.Ctx(), withPreset(opts, pid))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	plan.PresetID = pid
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

// TestOfficialPresetStripsTheBlock is the regression test for "restore the
// official source" writing an empty value instead of removing the block.
//
// A blank `registry=` in .npmrc is not npm's default — npm reads it as an
// invalid URL and every install fails with ERR_INVALID_URL (the same shape of
// breakage existed for cargo's `replace-with = ”`). The correct restore is to
// strip the managed block entirely, so the tool falls back to its own default.
//
// It also pins the read-back: with `+` instead of `*` in the Lua pattern, an
// empty value failed to match on its own line and the capture ran on to the
// tail sentinel, so status reported "# <<" as the configured value.
func TestOfficialPresetStripsTheBlock(t *testing.T) {
	sandbox(t)

	reg, diag, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	reportBroken(diag)

	cases := []struct {
		id    string
		file  string
		label string
	}{
		{"npm", ".npmrc", "registry"},
		{"pip", "pip/pip.conf", "index-url"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			p, ok := reg.Get(tc.id)
			if !ok {
				t.Fatalf("provider %s not found", tc.id)
			}
			m := app.New(reg)
			m.Cursor = indexOf(reg, tc.id)
			target := filepath.Join(paths.Home(), filepath.FromSlash(tc.file))

			// 1. Apply a real mirror: a managed block with a value appears.
			opts, pid, err := resolvePreset(p, presetOtherThan(p, "official"))
			if err != nil {
				t.Fatalf("resolvePreset: %v", err)
			}
			plan, err := p.Plan(m.Ctx(), withPreset(opts, pid))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if _, err := core.Apply(plan); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if st := p.Status(m.Ctx()); st.Current == "" {
				t.Fatalf("status after apply is empty; the block was not written")
			}

			// 2. Apply the official preset: the block must disappear.
			opts, pid, err = resolvePreset(p, "official")
			if err != nil {
				t.Fatalf("resolvePreset(official): %v", err)
			}
			plan, err = p.Plan(m.Ctx(), withPreset(opts, pid))
			if err != nil {
				t.Fatalf("Plan(official): %v", err)
			}
			plan.PresetID = pid
			if err := plan.Describe(); err != nil {
				t.Fatalf("Describe: %v", err)
			}
			if _, err := core.Apply(plan); err != nil {
				t.Fatalf("Apply(official): %v", err)
			}

			rest, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read after restore: %v", err)
			}
			if strings.Contains(string(rest), "keep-mirrors") {
				t.Errorf("the managed block survived the official preset:\n%s", rest)
			}

			// 3. status() must report unconfigured, never the tail sentinel.
			st := p.Status(m.Ctx())
			if st.Current != "" {
				t.Errorf("status after restore = %q, want empty (a sentinel line here means the read-back ran past an empty value)", st.Current)
			}

			// 4. The whole cycle is idempotent: restoring twice is a no-op.
			plan, err = p.Plan(m.Ctx(), withPreset(opts, pid))
			if err != nil {
				t.Fatalf("Plan(official) again: %v", err)
			}
			plan.PresetID = pid
			if err := plan.Describe(); err != nil {
				t.Fatalf("Describe: %v", err)
			}
			if !plan.Empty() {
				t.Errorf("restoring an already-restored file still produced %d change(s)", plan.ChangeCount())
			}
			_ = tc.label
		})
	}
}

// presetOtherThan returns the id of the first preset that is not "official",
// so the test applies a real mirror regardless of preset order.
func presetOtherThan(p provider.Provider, avoid string) string {
	for _, pr := range p.Presets() {
		if pr.ID != avoid {
			return pr.ID
		}
	}
	return ""
}

// TestStatusReadsEmptyValueAsEmpty covers a config file a *user* hand-edits
// into an empty value (the new restore path never writes one, but the block
// may predate the fix or be pasted from an old tutorial).
//
// With `+` in the Lua read-back pattern, an empty value failed to match on its
// own line and the capture ran past the newline onto the tail sentinel, so
// status reported "# <<" as the configured value. `*` matches it as empty.
func TestStatusReadsEmptyValueAsEmpty(t *testing.T) {
	sandbox(t)

	reg, diag, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	reportBroken(diag)

	cases := []struct {
		id    string
		file  string
		block string
	}{
		{"npm", ".npmrc", "# >>> keep-mirrors:npm >>>\nregistry=\n# <<< keep-mirrors:npm <<<\n"},
		{"pip", "pip/pip.conf", "# >>> keep-mirrors:pip >>>\n[global]\nindex-url = \n# <<< keep-mirrors:pip <<<\n"},
		{"go", ".config/go/env", "# >>> keep-mirrors:go >>>\nGOPROXY=\n# <<< keep-mirrors:go <<<\n"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			p, ok := reg.Get(tc.id)
			if !ok {
				t.Fatalf("provider %s not found", tc.id)
			}
			m := app.New(reg)
			m.Cursor = indexOf(reg, tc.id)

			target := filepath.Join(paths.Home(), filepath.FromSlash(tc.file))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(tc.block), 0o644); err != nil {
				t.Fatal(err)
			}

			// Env can outrank the file; make sure it is silent so the test
			// actually exercises the file read-back.
			for _, v := range []string{"npm_config_registry", "PIP_INDEX_URL", "GOPROXY"} {
				os.Unsetenv(v)
			}

			st := p.Status(m.Ctx())
			if strings.Contains(st.Current, "#") || strings.Contains(st.Current, "<<<") {
				t.Errorf("%s: status read-back = %q, captured the sentinel line instead of the empty value", tc.id, st.Current)
			}
		})
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
