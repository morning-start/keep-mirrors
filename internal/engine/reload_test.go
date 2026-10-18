package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"keep-mirrors/internal/engine"
)

// pluginSrc builds a minimal contract-v1 plugin with the given id and title, so
// a test can rewrite a file and observe a distinct result.
func pluginSrc(id, title string) string {
	return `local M = {}
M.api = { major = 1, minor = 0 }
M.id = "` + id + `"
M.title = "` + title + `"
M.version = "1.0.0"
M.presets = { { id = "only", label = "Only", opts = {} } }
M.settings = { { key = "k", type = "string", label = "K", default = "" } }
function M.status(ctx) return { installed = true, current = "", healthy = true } end
function M.plan(ctx, opts) return { changes = { { kind = "env", name = "KM_TEST", value = "1" } } } end
return M
`
}

// reloadFixture sets up two plugin directories and returns a loader pointed at
// them, plus the official dir for the test to write into.
func reloadFixture(t *testing.T) (*engine.Loader, string) {
	t.Helper()
	official := t.TempDir()
	user := t.TempDir()

	l := engine.NewLoader()
	l.OfficialDir = official
	l.UserDir = user
	l.Seed = nil // no seeding: these tests control the directory contents
	return l, official
}

func writePlugin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReloadFirstPollIsNotAChange is the baseline invariant: the poll that
// establishes the snapshot must not report a reload, or every start would
// announce a reload of plugins that were just loaded.
func TestReloadFirstPollIsNotAChange(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	_, _, changed, err := r.Poll()
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if changed {
		t.Error("the first poll reported a change for unchanged files")
	}
}

// TestReloadUnchangedDoesNotReload checks that polling an untouched directory
// is a no-op, which is the common case and must stay cheap.
func TestReloadUnchangedDoesNotReload(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		_, _, changed, err := r.Poll()
		if err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
		if changed {
			t.Fatalf("poll %d reported a change without any edit", i)
		}
	}
}

// TestReloadPicksUpEditedFile is the whole point: editing a plugin on disk
// changes what the next poll returns.
func TestReloadPicksUpEditedFile(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}

	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip RENAMED"))

	provs, _, changed, err := r.Poll()
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !changed {
		t.Fatal("an edited plugin did not trigger a reload")
	}
	if len(provs) != 1 {
		t.Fatalf("loaded %d providers, want 1", len(provs))
	}
	if got := provs[0].Title(); got != "Pip RENAMED" {
		t.Errorf("title = %q, want the edited value", got)
	}
	t.Cleanup(r.Close)
}

// TestReloadPicksUpNewAndDeletedFile covers added and removed plugins, which
// change the file set rather than any individual file.
func TestReloadPicksUpNewAndDeletedFile(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}

	// Added.
	writePlugin(t, official, "npm.lua", pluginSrc("npm", "Npm"))
	provs, _, changed, err := r.Poll()
	if err != nil {
		t.Fatalf("Poll after add: %v", err)
	}
	if !changed || len(provs) != 2 {
		t.Fatalf("added plugin: changed=%v n=%d, want true/2", changed, len(provs))
	}
	// Force the deferred close so the loaded set is released before the next
	// poll replaces it.
	r.Close()

	// Removed.
	if err := os.Remove(filepath.Join(official, "npm.lua")); err != nil {
		t.Fatal(err)
	}
	provs, _, changed, err = r.Poll()
	if err != nil {
		t.Fatalf("Poll after remove: %v", err)
	}
	if !changed || len(provs) != 1 {
		t.Fatalf("removed plugin: changed=%v n=%d, want true/1", changed, len(provs))
	}
	r.Close()
}

// TestReloadBrokenPluginKeepsOthers checks that a syntax error introduced while
// editing does not take the whole tool down: the good plugins still load and
// the broken one is reported.
func TestReloadBrokenPluginKeepsOthers(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))
	writePlugin(t, official, "npm.lua", pluginSrc("npm", "Npm"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}

	// Break npm, the way a half-saved buffer would.
	writePlugin(t, official, "npm.lua", "local M = { this is not lua")

	provs, diag, changed, err := r.Poll()
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !changed {
		t.Fatal("a broken edit did not trigger a reload")
	}
	if len(provs) != 1 || provs[0].ID() != "pip" {
		t.Errorf("loaded %d providers, want only the healthy pip", len(provs))
	}
	var found bool
	for _, d := range diag {
		if d.ID == "npm" && d.Err != nil {
			found = true
		}
	}
	if !found {
		t.Errorf("the broken plugin was not reported: %+v", diag)
	}
	r.Close()
}

// TestReloadAllBrokenKeepsPreviousSet checks the failure floor: when every
// plugin fails to compile, the reload is refused so the caller keeps working
// with the last good set instead of an empty list.
func TestReloadAllBrokenKeepsPreviousSet(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}

	writePlugin(t, official, "pip.lua", "this is not lua at all")

	provs, _, changed, err := r.Poll()
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if changed {
		t.Error("a fully broken set was installed as a reload")
	}
	if provs != nil {
		t.Errorf("Poll returned %d providers for a refused reload", len(provs))
	}
	r.Close()
}

// TestReloadUserOverridesOfficial checks that precedence is honoured by the
// watcher too: editing the shadowed official file must not report a change,
// because that edit has no effect on what actually loads.
func TestReloadUserOverridesOfficial(t *testing.T) {
	official := t.TempDir()
	user := t.TempDir()

	l := engine.NewLoader()
	l.OfficialDir = official
	l.UserDir = user
	l.Seed = nil

	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Official"))
	writePlugin(t, user, "pip.lua", pluginSrc("pip", "User"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}

	// Edit the shadowed official copy: no observable effect.
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Official EDITED"))
	if _, _, changed, _ := r.Poll(); changed {
		t.Error("editing a shadowed official plugin reported a change")
	}

	// Edit the user copy: this one must reload.
	writePlugin(t, user, "pip.lua", pluginSrc("pip", "User EDITED"))
	provs, _, changed, err := r.Poll()
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !changed {
		t.Fatal("editing the winning user plugin did not reload")
	}
	if len(provs) != 1 || provs[0].Title() != "User EDITED" {
		t.Errorf("reload did not pick up the user override")
	}
	r.Close()
}

// TestReloadMissingDirsIsNotAnError covers a machine where the plugin
// directories do not exist yet.
func TestReloadMissingDirsIsNotAnError(t *testing.T) {
	l := engine.NewLoader()
	l.OfficialDir = filepath.Join(t.TempDir(), "nope")
	l.UserDir = filepath.Join(t.TempDir(), "also-nope")
	l.Seed = nil

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatalf("Snapshot on missing dirs: %v", err)
	}
	if _, _, changed, err := r.Poll(); err != nil {
		t.Fatalf("Poll on missing dirs: %v", err)
	} else if changed {
		t.Error("missing directories reported a change")
	}
	r.Close()
}

// TestReloadRetiresOldProvidersLazily documents the lifetime rule: the set
// going out of use is not closed at replacement time, because a call may still
// be running on its VMs.
func TestReloadRetiresOldProvidersLazily(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}

	// The set loaded at startup is what the first reload replaces.
	startup, _, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	r.Adopt(startup)

	// The reloader compares (mtime, size). Two edits of the same length can
	// land in the same timestamp tick, which would make the second poll see
	// "no change" and the test would fail one run in ten. Padding the title
	// makes the sizes differ, so every poll is guaranteed to observe it.
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip2"))
	first, _, changed, err := r.Poll()
	if err != nil || !changed {
		t.Fatalf("first poll: changed=%v err=%v", changed, err)
	}

	// The replaced set must still work: a call in flight would use it.
	if _, err := startup[0].Plan(testCtx(), map[string]any{}); err != nil {
		t.Fatalf("replaced provider was closed too early: %v", err)
	}

	// The next poll closes it.
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip3-length-differs"))
	second, _, changed, err := r.Poll()
	if err != nil || !changed {
		t.Fatalf("second poll: changed=%v err=%v", changed, err)
	}
	// After that poll, the startup set must be closed: status() surfaces the
	// closed state instead of panicking.
	if st := startup[0].Status(testCtx()); !strings.Contains(st.Note, "closed") {
		t.Errorf("replaced provider note = %q, want it to be closed by the next poll", st.Note)
	}

	// The set installed by the *second* poll (the live one) must survive that
	// close, and so must the one before it: only the replaced set is released.
	if st := second[0].Status(testCtx()); strings.Contains(st.Note, "closed") {
		t.Errorf("the live set was closed by the poll that replaced the previous one: %q", st.Note)
	}
	if st := first[0].Status(testCtx()); strings.Contains(st.Note, "closed") {
		t.Errorf("the set installed one poll ago was closed early: %q", st.Note)
	}
	r.Close()
}

// TestReloadLiveSetSurvivesIdlePoll is the regression test for a bug that made
// every reload expire one second later.
//
// The caller used to retire the slice Poll had just returned, which is the
// *incoming* set, so the next idle poll closed the providers the UI was
// displaying. The symptom looked like the watcher working — the title changed
// on reload — followed by every provider failing one tick later, which is easy
// to misread as a plugin error rather than a lifetime bug.
func TestReloadLiveSetSurvivesIdlePoll(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}
	startup, _, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	r.Adopt(startup)

	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip2"))
	live, _, changed, err := r.Poll()
	if err != nil || !changed {
		t.Fatalf("poll: changed=%v err=%v", changed, err)
	}

	// Nothing changes on disk, so the next poll has no work. It must not close
	// the set it is displaying.
	for i := 0; i < 3; i++ {
		if _, _, changed, err := r.Poll(); err != nil {
			t.Fatalf("idle poll %d: %v", i, err)
		} else if changed {
			t.Fatalf("idle poll %d reported a spurious change", i)
		}
		if st := live[0].Status(testCtx()); strings.Contains(st.Note, "closed") {
			t.Fatalf("idle poll %d closed the live provider set: %q", i, st.Note)
		}
	}

	// The replacement set is still closed on schedule.
	if st := startup[0].Status(testCtx()); !strings.Contains(st.Note, "closed") {
		t.Errorf("the replaced set was never closed: %q", st.Note)
	}
	r.Close()
}

// TestReloadAdoptIsWhatCloseReleases checks the startup wiring: a set handed to
// Adopt must be released by Close, or a long-running TUI leaks one VM per file
// it never re-loads.
func TestReloadAdoptIsWhatCloseReleases(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}
	startup, _, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	r.Adopt(startup)
	r.Close()

	if st := startup[0].Status(testCtx()); !strings.Contains(st.Note, "closed") {
		t.Errorf("Close did not release the adopted set: %q", st.Note)
	}
}

// TestReloadStatusAfterCloseIsSafe checks that calling into a closed provider
// fails cleanly rather than panicking, which is what a late benchmark would do.
func TestReloadStatusAfterCloseIsSafe(t *testing.T) {
	l, official := reloadFixture(t)
	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip"))

	r := engine.NewReloader(l)
	if err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}

	writePlugin(t, official, "pip.lua", pluginSrc("pip", "Pip2"))
	first, _, _, err := r.Poll()
	if err != nil {
		t.Fatal(err)
	}
	r.Close() // closes the live set

	// A closed provider must not panic: status() maps the error into Note.
	st := first[0].Status(testCtx())
	if st.Note == "" {
		t.Error("a closed provider reported no error note")
	}
	if !strings.Contains(st.Note, "closed") {
		t.Errorf("note = %q, want it to mention the closed state", st.Note)
	}
}
