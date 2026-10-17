package main

import (
	"os"
	"path/filepath"
	"testing"

	"keep-mirrors/internal/engine"
	"keep-mirrors/internal/paths"
)

// TestHotReloadSeesDiskEdit is the end-to-end check for the whole point of the
// disk-based plugin model: editing a plugin file changes what the tool runs,
// without a rebuild and without a restart.
//
// It deliberately goes through the real Loader and Reloader over the real
// (sandboxed) AppData directories, rather than a fixture, so a break in the
// path resolution or the seeding step fails here too.
func TestHotReloadSeesDiskEdit(t *testing.T) {
	sandbox(t)

	loader := newLoader()
	if _, err := loader.SeedIfEmpty(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	provs, _, err := loader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	r := engine.NewReloader(loader)
	if err := r.Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer r.Close()

	// Steady state: nothing to do.
	if _, _, changed, err := r.Poll(); err != nil {
		t.Fatalf("Poll: %v", err)
	} else if changed {
		t.Fatal("an unchanged plugin directory reported a reload")
	}

	// Edit the seeded pip plugin the way a user would.
	official := loader.OfficialPluginDir()
	pip := filepath.Join(official, "pip.lua")
	body, err := os.ReadFile(pip)
	if err != nil {
		t.Fatalf("read pip.lua: %v", err)
	}
	edited := replaceOnce(string(body), `M.title     = "Python / pip"`, `M.title     = "Python / RELOADED"`)
	if edited == string(body) {
		t.Fatal("the test's edit did not match anything; the plugin header changed")
	}
	if err := os.WriteFile(pip, []byte(edited), 0o644); err != nil {
		t.Fatalf("write pip.lua: %v", err)
	}

	reloaded, _, changed, err := r.Poll()
	if err != nil {
		t.Fatalf("Poll after edit: %v", err)
	}
	if !changed {
		t.Fatal("editing a plugin on disk did not trigger a reload")
	}

	var found bool
	for _, p := range reloaded {
		if p.ID() == "pip" {
			found = true
			if got := p.Title(); got != "Python / RELOADED" {
				t.Errorf("title = %q, want the edited value", got)
			}
		}
	}
	if !found {
		t.Fatal("pip disappeared after the reload")
	}

	// The retired set (the ones loaded before the edit) must not be closed
	// until the next poll, so a call in flight can finish.
	r.Retire(reloaded)
	r.Close()
	_ = provs
}

// TestHotReloadUserDirOverride checks that dropping a plugin into user/ is
// picked up and wins over the official copy of the same id.
func TestHotReloadUserDirOverride(t *testing.T) {
	sandbox(t)

	loader := newLoader()
	if _, err := loader.SeedIfEmpty(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := engine.NewReloader(loader)
	if err := r.Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer r.Close()

	official := loader.OfficialPluginDir()
	user := loader.UserPluginDir()
	if err := os.MkdirAll(user, 0o755); err != nil {
		t.Fatalf("mkdir user: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(official, "pip.lua"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	edited := replaceOnce(string(body), `M.title     = "Python / pip"`, `M.title     = "Python / FROM-USER"`)
	if err := os.WriteFile(filepath.Join(user, "pip.lua"), []byte(edited), 0o644); err != nil {
		t.Fatalf("write user pip.lua: %v", err)
	}

	reloaded, _, changed, err := r.Poll()
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !changed {
		t.Fatal("a new user plugin did not trigger a reload")
	}
	for _, p := range reloaded {
		if p.ID() == "pip" && p.Title() != "Python / FROM-USER" {
			t.Errorf("pip title = %q, want the user override to win", p.Title())
		}
	}
	r.Retire(reloaded)
}

// TestSeededPluginsAreRealFiles checks that the runtime source really is on
// disk: the whole hot reload design depends on it.
func TestSeededPluginsAreRealFiles(t *testing.T) {
	sandbox(t)

	loader := newLoader()
	if _, err := loader.SeedIfEmpty(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	official := loader.OfficialPluginDir()
	entries, err := os.ReadDir(official)
	if err != nil {
		t.Fatalf("read official dir: %v", err)
	}
	if len(entries) != 4 {
		t.Errorf("official dir holds %d files, want the 4 seeded plugins", len(entries))
	}
	for _, name := range []string{"pip.lua", "npm.lua", "cargo.lua", "go.lua"} {
		p := filepath.Join(official, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is not a real file: %v", name, err)
		}
	}

	// The user directory is created on demand, not by seeding.
	if _, err := os.Stat(loader.UserPluginDir()); !os.IsNotExist(err) {
		t.Error("seeding created the user directory; it should be left to the user")
	}
	_ = paths.OfficialPluginDir()
}

// replaceOnce replaces the first occurrence of old with new.
func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
