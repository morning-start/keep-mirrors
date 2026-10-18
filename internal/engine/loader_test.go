package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"keep-mirrors/internal/engine"
	"keep-mirrors/internal/paths"
)

// seedSandbox points the plugin root at a temp dir so a test never seeds into
// the developer's real AppData.
//
// The cache reset is registered BEFORE the Setenv calls on purpose: t.Cleanup
// runs LIFO, and the package caches the resolved home. Registered last, Reset
// ran while the environment still pointed at the temp dir, so every later test
// in this package inherited a home that no longer existed.
func seedSandbox(t *testing.T) string {
	t.Helper()
	appData := t.TempDir()
	t.Cleanup(paths.Reset)
	t.Setenv("APPDATA", appData)
	t.Setenv("HOME", appData)
	t.Setenv("USERPROFILE", appData)
	// POSIX resolves the app dir through os.UserConfigDir, which prefers
	// XDG_CONFIG_HOME over $HOME/.config. Leaving it inherited put the plugin
	// directory in the runner's real config dir while home pointed at the temp
	// one, so every package in the CI run shared a plugin set it never wrote.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(appData, ".config"))
	paths.Reset()
	if dir := paths.OfficialPluginDir(); !strings.HasPrefix(dir, appData) {
		t.Fatalf("sandbox did not take: plugin dir %q is outside %q", dir, appData)
	}
	return appData
}

// fakeSeed builds an in-memory seed FS with the given files.
func fakeSeed(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

// TestSeedWritesWhenOfficialIsEmpty covers the first-run path: an install with
// no plugins yet must end up able to work offline.
func TestSeedWritesWhenOfficialIsEmpty(t *testing.T) {
	seedSandbox(t)

	l := engine.NewLoader()
	l.Seed = fakeSeed(map[string]string{
		"pip.lua": "return { id = 'pip' }",
		"npm.lua": "return { id = 'npm' }",
	})

	n, err := l.SeedIfEmpty()
	if err != nil {
		t.Fatalf("SeedIfEmpty: %v", err)
	}
	if n != 2 {
		t.Errorf("seeded %d files, want 2", n)
	}
	for _, name := range []string{"pip.lua", "npm.lua"} {
		if _, err := os.Stat(filepath.Join(l.OfficialPluginDir(), name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}
}

// TestSeedDoesNotOverwriteExisting is the invariant that makes seeding safe to
// call on every start: once official/ has content, the embedded snapshot must
// stay out of the way so a registry sync or a deliberate deletion is not undone
// on the next launch.
func TestSeedDoesNotOverwriteExisting(t *testing.T) {
	seedSandbox(t)

	l := engine.NewLoader()
	l.Seed = fakeSeed(map[string]string{"pip.lua": "return { id = 'pip' } -- seeded"})

	if _, err := l.SeedIfEmpty(); err != nil {
		t.Fatalf("first seed: %v", err)
	}

	// Simulate the registry sync installing a newer version, or the user
	// editing the file.
	synced := "return { id = 'pip' } -- synced from registry"
	dst := filepath.Join(l.OfficialPluginDir(), "pip.lua")
	if err := os.WriteFile(dst, []byte(synced), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := l.SeedIfEmpty()
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if n != 0 {
		t.Errorf("seeded %d files into a non-empty directory, want 0", n)
	}

	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != synced {
		t.Errorf("existing plugin was overwritten:\n got: %s\nwant: %s", body, synced)
	}
}

// TestSeedRespectsDeletedPlugins checks the other half of "do not overwrite":
// a user who deletes an official plugin must not have it resurrected.
func TestSeedRespectsDeletedPlugins(t *testing.T) {
	seedSandbox(t)

	l := engine.NewLoader()
	l.Seed = fakeSeed(map[string]string{
		"pip.lua": "return { id = 'pip' }",
		"npm.lua": "return { id = 'npm' }",
	})
	if _, err := l.SeedIfEmpty(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Delete one plugin but leave the other, so the directory is still non-empty.
	if err := os.Remove(filepath.Join(l.OfficialPluginDir(), "pip.lua")); err != nil {
		t.Fatal(err)
	}

	if _, err := l.SeedIfEmpty(); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.OfficialPluginDir(), "pip.lua")); !os.IsNotExist(err) {
		t.Error("a deleted plugin was resurrected by seeding")
	}
}

// TestListUserOverridesOfficial locks the precedence rule: a user plugin with
// the same id replaces the official one, which is how someone pins a local fix
// without patching the curated copy.
func TestListUserOverridesOfficial(t *testing.T) {
	seedSandbox(t)

	official := t.TempDir()
	user := t.TempDir()
	for _, f := range []struct{ dir, name string }{
		{official, "pip.lua"},
		{official, "npm.lua"},
		{user, "pip.lua"},
	} {
		if err := os.WriteFile(filepath.Join(f.dir, f.name), []byte("return {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	l := engine.NewLoader()
	l.OfficialDir = official
	l.UserDir = user

	byID := map[string]engine.PluginSource{}
	for _, s := range l.List() {
		byID[s.ID] = s
	}
	if len(byID) != 2 {
		t.Fatalf("List() returned %d plugins, want 2 (pip overriding, npm official)", len(byID))
	}
	if got := byID["pip"].Origin; got != engine.OriginUser {
		t.Errorf("pip origin = %q, want user", got)
	}
	if got := byID["npm"].Origin; got != engine.OriginOfficial {
		t.Errorf("npm origin = %q, want official", got)
	}
}

// TestListMissingDirsIsNotAnError covers a fresh install where neither
// directory exists yet: listing must return empty rather than fail.
func TestListMissingDirsIsNotAnError(t *testing.T) {
	seedSandbox(t)

	l := engine.NewLoader()
	l.OfficialDir = filepath.Join(t.TempDir(), "nope")
	l.UserDir = filepath.Join(t.TempDir(), "also-nope")

	if got := l.List(); len(got) != 0 {
		t.Errorf("List() = %v, want empty for missing directories", got)
	}
}
