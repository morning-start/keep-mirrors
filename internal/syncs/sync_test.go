package syncs

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"keep-mirrors/internal/paths"
	"keep-mirrors/internal/plugins"
)

// fakeRemote serves plugin files from memory, so these tests never touch the
// network. It records calls so a test can assert that a no-op sync really did
// not download anything.
type fakeRemote struct {
	sha   string
	files map[string]string
	err   error

	fileCalls int
}

func (f *fakeRemote) BranchSHA(context.Context, string, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.sha, nil
}

func (f *fakeRemote) File(_ context.Context, _, _, path string) ([]byte, error) {
	f.fileCalls++
	if f.err != nil {
		return nil, f.err
	}
	body, ok := f.files[path]
	if !ok {
		// Match the real transport: a missing file is the not-found sentinel,
		// not a generic error, so the drop-a-source guard is exercised.
		return nil, fmt.Errorf("%s: %w", path, ErrNotFound)
	}
	return []byte(body), nil
}

// validPlugin is a minimal plugin that satisfies the contract, written for id.
func validPlugin(id, version string) string {
	return `local M = {}
M.api = { major = 1, minor = 0 }
M.id = "` + id + `"
M.title = "` + id + `"
M.version = "` + version + `"
M.presets = { { id = "only", label = "Only", opts = {} } }
M.settings = { { key = "k", type = "string", label = "K", default = "" } }
function M.status(ctx) return { installed = true, current = "", healthy = true } end
function M.plan(ctx, opts) return { changes = {} } end
return M
`
}

// fullSet builds a complete set of the plugins the binary ships.
func fullSet(version string) map[string]string {
	m := map[string]string{}
	for _, id := range knownPlugins {
		m[PluginDirInRepo+"/"+id+".lua"] = validPlugin(id, version)
	}
	return m
}

// sandbox points AppDir at a temp dir so tests never read the developer's real
// sync state or write into their real plugin directory.
//
// Reset is registered before the Setenv calls because t.Cleanup runs LIFO;
// otherwise the package caches the temp home and every later test inherits it.
func sandbox(t *testing.T) string {
	t.Helper()
	appData := t.TempDir()
	t.Cleanup(paths.Reset)
	t.Setenv("APPDATA", appData)
	t.Setenv("HOME", appData)
	t.Setenv("USERPROFILE", appData)
	// See engine.seedSandbox: os.UserConfigDir prefers XDG_CONFIG_HOME, so an
	// inherited value would point the plugin dir at the real config root.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(appData, ".config"))
	paths.Reset()
	dir := paths.OfficialPluginDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testConfig(dir string) Config {
	return Config{Repo: "owner/repo", Branch: "plugins", Dir: dir}
}

// TestSyncFirstRunWritesEveryFile covers the primary path.
func TestSyncFirstRunWritesEveryFile(t *testing.T) {
	dir := sandbox(t)
	sha := strings.Repeat("a", 40)
	r := &fakeRemote{sha: sha, files: fullSet("1.2.0")}

	res, err := run(context.Background(), r, testConfig(dir), Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Updated {
		t.Error("a first sync reported no update")
	}
	if res.SHA != sha {
		t.Errorf("SHA = %q, want %q", res.SHA, sha)
	}
	if res.Version != "1.2.0" {
		t.Errorf("Version = %q, want 1.2.0", res.Version)
	}
	for _, id := range knownPlugins {
		p := filepath.Join(dir, id+".lua")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was not written: %v", id, err)
		}
	}

	st, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.SHA != sha {
		t.Errorf("saved state SHA = %q, want %q", st.SHA, sha)
	}
}

// TestSyncSameSHAIsANoOp is the quota saver: an unchanged branch must not
// download a single file.
func TestSyncSameSHAIsANoOp(t *testing.T) {
	dir := sandbox(t)
	sha := strings.Repeat("b", 40)
	r := &fakeRemote{sha: sha, files: fullSet("1.0.0")}

	if _, err := run(context.Background(), r, testConfig(dir), Options{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	r.fileCalls = 0

	res, err := run(context.Background(), r, testConfig(dir), Options{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Updated {
		t.Error("an unchanged SHA reported an update")
	}
	if r.fileCalls != 0 {
		t.Errorf("downloaded %d files for an unchanged SHA, want 0", r.fileCalls)
	}
}

// TestSyncForceRedownloads covers the escape hatch for a corrupted local copy.
func TestSyncForceRedownloads(t *testing.T) {
	dir := sandbox(t)
	sha := strings.Repeat("c", 40)
	r := &fakeRemote{sha: sha, files: fullSet("1.0.0")}

	if _, err := run(context.Background(), r, testConfig(dir), Options{}); err != nil {
		t.Fatal(err)
	}
	r.fileCalls = 0

	res, err := run(context.Background(), r, testConfig(dir), Options{Force: true})
	if err != nil {
		t.Fatalf("forced run: %v", err)
	}
	if !res.Updated {
		t.Error("Force did not re-download")
	}
	if r.fileCalls == 0 {
		t.Error("Force downloaded nothing")
	}
}

// TestSyncDryRunWritesNothing checks that --dry-run is truly read-only.
func TestSyncDryRunWritesNothing(t *testing.T) {
	dir := sandbox(t)
	r := &fakeRemote{sha: strings.Repeat("d", 40), files: fullSet("1.0.0")}

	res, err := run(context.Background(), r, testConfig(dir), Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.Updated {
		t.Error("a dry run reported an update")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a dry run wrote %d files", len(entries))
	}
	if stateFileExists() {
		t.Error("a dry run recorded sync state")
	}
}

// TestSyncRejectsIncompatibleContract is the guard the plugin branch depends
// on: a plugin targeting a future contract must never reach disk.
func TestSyncRejectsIncompatibleContract(t *testing.T) {
	dir := sandbox(t)
	files := fullSet("1.0.0")
	// One plugin declares a contract this binary cannot speak.
	files[PluginDirInRepo+"/pip.lua"] = strings.Replace(
		validPlugin("pip", "1.0.0"), "major = 1", "major = 2", 1)

	r := &fakeRemote{sha: strings.Repeat("e", 40), files: files}

	_, err := run(context.Background(), r, testConfig(dir), Options{})
	if err == nil {
		t.Fatal("an incompatible plugin was accepted")
	}
	if !strings.Contains(err.Error(), "contract") {
		t.Errorf("error = %v, want it to mention the contract", err)
	}
	assertDirEmpty(t, dir, "a rejected sync left files behind")
	if stateFileExists() {
		t.Error("a rejected sync recorded state")
	}
}

// TestSyncRejectsBrokenLua checks a syntax error is caught before writing.
func TestSyncRejectsBrokenLua(t *testing.T) {
	dir := sandbox(t)
	files := fullSet("1.0.0")
	files[PluginDirInRepo+"/npm.lua"] = "local M = { this is not lua"

	r := &fakeRemote{sha: strings.Repeat("f", 40), files: files}
	if _, err := run(context.Background(), r, testConfig(dir), Options{}); err == nil {
		t.Fatal("a plugin that does not compile was accepted")
	}
	assertDirEmpty(t, dir, "a rejected sync left files behind")
}

// TestSyncRejectsIDMismatch checks that a renamed file cannot smuggle in a
// different plugin's id, which would break the managed-block convention.
func TestSyncRejectsIDMismatch(t *testing.T) {
	dir := sandbox(t)
	files := fullSet("1.0.0")
	files[PluginDirInRepo+"/pip.lua"] = validPlugin("notpip", "1.0.0")

	r := &fakeRemote{sha: strings.Repeat("1", 40), files: files}
	_, err := run(context.Background(), r, testConfig(dir), Options{})
	if err == nil {
		t.Fatal("a mismatched id was accepted")
	}
	if !strings.Contains(err.Error(), "declares id") {
		t.Errorf("error = %v, want it to mention the id mismatch", err)
	}
}

// TestSyncRefusesToDropShippedSources checks the removal guard. A plugin the
// branch no longer carries is an upstream mistake far more often than a
// deliberate removal, so the sync must fail rather than quietly make a source
// disappear from the user's tool.
func TestSyncRefusesToDropShippedSources(t *testing.T) {
	dir := sandbox(t)
	files := fullSet("1.0.0")
	delete(files, PluginDirInRepo+"/cargo.lua")

	r := &fakeRemote{sha: strings.Repeat("2", 40), files: files}
	_, err := run(context.Background(), r, testConfig(dir), Options{})
	if err == nil {
		t.Fatal("a set missing a shipped plugin was accepted")
	}
	// The guard must name the dropped source. Before the not-found sentinel
	// existed this reported a bare HTTP 404, because the download aborted
	// before the completeness check ever ran.
	if !strings.Contains(err.Error(), "cargo") {
		t.Errorf("error %v does not name the missing plugin", err)
	}
	if !strings.Contains(err.Error(), "refusing to drop") {
		t.Errorf("error %v is not the drop-a-source refusal", err)
	}
	assertDirEmpty(t, dir, "a refused sync left files behind")
	if stateFileExists() {
		t.Error("a refused sync recorded state")
	}
}

// TestSetVersionIsNotAStringComparison is the regression test for reporting the
// wrong version: comparing version strings with ">" says 1.9.0 is newer than
// 1.10.0, which is false for every real version scheme.
func TestSetVersionIsNotAStringComparison(t *testing.T) {
	if got := setVersion(map[string]bool{"1.10.0": true}); got != "1.10.0" {
		t.Errorf("single version = %q, want 1.10.0", got)
	}
	// A branch that bumps one plugin and leaves the rest is legitimate; the
	// honest answer is that the set is mixed, not that one of them is newest.
	got := setVersion(map[string]bool{"1.9.0": true, "1.10.0": true})
	if !strings.Contains(got, "mixed") {
		t.Errorf("mixed versions = %q, want it to say the set is mixed", got)
	}
	if !strings.Contains(got, "1.10.0") || !strings.Contains(got, "1.9.0") {
		t.Errorf("mixed versions = %q, want both listed", got)
	}
	if got := setVersion(map[string]bool{}); got != "" {
		t.Errorf("no versions = %q, want empty", got)
	}
}

// TestSyncReportsVersionFromTheModule checks the version is read from the loaded
// module rather than re-parsed out of the source text, so it cannot describe a
// plugin other than the one that was validated.
func TestSyncReportsVersionFromTheModule(t *testing.T) {
	dir := sandbox(t)
	r := &fakeRemote{sha: strings.Repeat("3", 40), files: fullSet("2.0.1")}

	res, err := run(context.Background(), r, testConfig(dir), Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Version != "2.0.1" {
		t.Errorf("Version = %q, want 2.0.1", res.Version)
	}
}

// TestSyncPropagatesRemoteError checks that a network failure is reported and
// nothing is written.
func TestSyncPropagatesRemoteError(t *testing.T) {
	dir := sandbox(t)
	r := &fakeRemote{err: os.ErrDeadlineExceeded}

	if _, err := run(context.Background(), r, testConfig(dir), Options{}); err == nil {
		t.Fatal("a remote error was swallowed")
	}
	assertDirEmpty(t, dir, "a failed sync left files behind")
}

// TestValidSHA checks the input guard that keeps an unchecked value out of a
// URL.
func TestValidSHA(t *testing.T) {
	ok := strings.Repeat("a", 40)
	if !ValidSHA(ok) {
		t.Error("a valid SHA was rejected")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("A", 40), strings.Repeat("g", 40),
		"main", strings.Repeat("a", 39), strings.Repeat("a", 41)} {
		if ValidSHA(bad) {
			t.Errorf("%q was accepted as a SHA", bad)
		}
	}
}

// TestSyncStateSurvivesCorruption checks that a damaged state file degrades to
// "never synced" instead of blocking the sync forever.
func TestSyncStateSurvivesCorruption(t *testing.T) {
	sandbox(t)
	p := filepath.Join(paths.AppDir(), "plugins-sync.json")
	if err := os.MkdirAll(paths.AppDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState on corrupt file: %v", err)
	}
	// A corrupt file falls back to the seed, so the install still knows which
	// snapshot it carries. What must not happen is an error, which would make
	// sync impossible to run at all.
	if st.SHA == "" {
		t.Error("a corrupt state file left the install with no known snapshot")
	}
	if !st.FromSeed {
		t.Error("the fallback did not identify itself as coming from the seed")
	}
}

// TestCuratedNamesMatchSeed locks the known set to what the binary ships: if a
// plugin is added to or removed from internal/plugins, this fails until
// knownPlugins follows.
func TestCuratedNamesMatchSeed(t *testing.T) {
	seed, err := seedIDs()
	if err != nil {
		t.Fatalf("enumerate seed: %v", err)
	}
	known := map[string]bool{}
	for _, id := range knownPlugins {
		known[id] = true
	}
	for _, id := range seed {
		if !known[id] {
			t.Errorf("seed plugin %q is missing from knownPlugins", id)
		}
	}
	for _, id := range knownPlugins {
		found := false
		for _, s := range seed {
			if s == id {
				found = true
			}
		}
		if !found {
			t.Errorf("knownPlugins lists %q but the seed has no such plugin", id)
		}
	}
}

// seedIDs lists the plugin ids in the embedded seed.
func seedIDs() ([]string, error) {
	entries, err := fs.ReadDir(plugins.FS, ".")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".lua") {
			out = append(out, strings.TrimSuffix(e.Name(), ".lua"))
		}
	}
	return out, nil
}

// assertDirEmpty fails when dir holds any .lua file.
func assertDirEmpty(t *testing.T, dir, msg string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var got []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lua") {
			got = append(got, e.Name())
		}
	}
	if len(got) > 0 {
		t.Errorf("%s: %v", msg, got)
	}
}

// stateFileExists reports whether a sync state file has been written.
//
// Assertions about "nothing was recorded" use this rather than LoadState().SHA:
// with no state file LoadState falls back to the embedded snapshot, which is a
// real SHA and is not evidence that a sync happened.
func stateFileExists() bool {
	_, err := os.Stat(filepath.Join(paths.AppDir(), "plugins-sync.json"))
	return err == nil
}
