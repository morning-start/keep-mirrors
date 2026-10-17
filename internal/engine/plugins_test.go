package engine_test

import (
	"io/fs"
	"strings"
	"testing"

	"keep-mirrors/internal/engine"
	"keep-mirrors/internal/plugins"
	"keep-mirrors/internal/provider"
)

// seedNames returns the .lua files in the embedded seed snapshot.
//
// The test reads the same fs.FS the binary seeds from, not a path on disk.
// That is the point: when the test read `../../plugins` while the binary read
// its own embed copy, the two could drift and the suite stayed green while the
// shipped behaviour was stale.
func seedNames(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(plugins.FS, ".")
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".lua") {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatal("the seed snapshot contains no .lua files")
	}
	return out
}

func loadSeed(t *testing.T, name string) *engine.LuaProvider {
	t.Helper()
	body, err := fs.ReadFile(plugins.FS, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	p, _, err := engine.Load(string(body), engine.DefaultOptions())
	if err != nil {
		t.Fatalf("%s: Load: %v", name, err)
	}
	t.Cleanup(p.Close)
	return p
}

// TestSeedPluginsLoad runs every curated plugin through the same loader the
// binary uses. A plugin that stops compiling, or that changes its field names,
// fails here rather than at runtime in front of the user.
func TestSeedPluginsLoad(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range seedNames(t) {
		id := strings.TrimSuffix(name, ".lua")
		t.Run(id, func(t *testing.T) {
			p := loadSeed(t, name)

			if p.ID() != id {
				t.Errorf("file %q declares id %q", name, p.ID())
			}
			if seen[p.ID()] {
				t.Errorf("duplicate plugin id %q", p.ID())
			}
			seen[p.ID()] = true

			m := p.Meta()
			if m.API.Major != engine.ContractMajor {
				t.Errorf("api.major = %d, want %d", m.API.Major, engine.ContractMajor)
			}
			if m.Version == "" {
				t.Error("version is empty; a distributed plugin needs its own release number")
			}
			if m.Author == "" || m.License == "" {
				t.Errorf("author/license missing: author=%q license=%q", m.Author, m.License)
			}
			if m.Description == "" {
				t.Error("description is empty")
			}
			if len(p.Presets()) == 0 {
				t.Error("plugin declares no presets")
			}
			if len(p.Settings()) == 0 {
				t.Error("plugin declares no settings")
			}

			st := p.Status(testCtx())
			t.Logf("status: installed=%v current=%q", st.Installed, st.Current)
			if strings.HasPrefix(st.Note, "status failed") {
				t.Errorf("status() errored: %s", st.Note)
			}

			plan, err := p.Plan(testCtx(), p.Presets()[0].Opts)
			if err != nil {
				t.Fatalf("Plan(%s): %v", p.Presets()[0].ID, err)
			}
			if plan.Empty() {
				t.Errorf("preset %q produced an empty plan", p.Presets()[0].ID)
			}
		})
	}
}

// TestSeedPluginsPlanOfficial checks the "restore the official source" path,
// which is the one most likely to be wrong: it has to both clear the variable
// and blank the config entry, or a stale value survives.
func TestSeedPluginsPlanOfficial(t *testing.T) {
	for _, name := range seedNames(t) {
		p := loadSeed(t, name)

		var official map[string]any
		for _, pr := range p.Presets() {
			if pr.ID == "official" {
				official = pr.Opts
			}
		}
		if official == nil {
			t.Logf("%s: no official preset, skipping", name)
			continue
		}
		plan, err := p.Plan(testCtx(), official)
		if err != nil {
			t.Fatalf("%s: Plan(official): %v", name, err)
		}
		if plan.Empty() {
			t.Errorf("%s: official preset produced an empty plan", name)
		}
	}
}

func testCtx() provider.Context {
	return provider.Context{
		Platform: "windows",
		Home:     "C:\\Users\\test",
		Shell:    "powershell",
		Values:   map[string]any{},
	}
}
