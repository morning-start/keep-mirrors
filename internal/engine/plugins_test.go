package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuiltinPluginsLoad runs every plugin shipped in plugins/ through the
// same loader the binary uses. This is the test that keeps the Lua contract
// and the Go bridge in sync: a plugin that stops compiling, or that changes
// its field names, fails here rather than at runtime in front of the user.
func TestBuiltinPluginsLoad(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "plugins"))
	if err != nil {
		t.Fatalf("read plugins dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no plugins found in plugins/")
	}

	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".lua")
		t.Run(id, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", "plugins", e.Name()))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			p, _, err := Load(string(body), DefaultOptions())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			defer p.Close()

			if p.ID() != id {
				t.Errorf("plugin file %q declares id %q", e.Name(), p.ID())
			}
			if seen[p.ID()] {
				t.Errorf("duplicate plugin id %q", p.ID())
			}
			seen[p.ID()] = true

			if len(p.Presets()) == 0 {
				t.Error("plugin declares no presets")
			}
			if len(p.Settings()) == 0 {
				t.Error("plugin declares no settings")
			}

			// status() must run without erroring, even when the toolchain
			// the plugin manages is not installed on this machine.
			st := p.Status(testCtx())
			t.Logf("status: installed=%v current=%q note=%q",
				st.Installed, st.Current, st.Note)
			if st.Note != "" && strings.HasPrefix(st.Note, "status failed") {
				t.Errorf("status() errored: %s", st.Note)
			}

			// plan() must produce at least one change for the first preset,
			// which is the path the UI exercises first.
			if len(p.Presets()) > 0 {
				plan, err := p.Plan(testCtx(), p.Presets()[0].Opts)
				if err != nil {
					t.Fatalf("Plan(%s): %v", p.Presets()[0].ID, err)
				}
				if plan.Empty() {
					t.Errorf("preset %q produced an empty plan", p.Presets()[0].ID)
				}
				t.Logf("plan(%s): %s", p.Presets()[0].ID, plan.Summary())
			}
		})
	}
}

// TestBuiltinPluginsPlanOfficial checks the "restore the official source"
// path, which is the one most likely to be wrong: it has to both clear the
// variable and blank the config entry, or a stale value survives.
func TestBuiltinPluginsPlanOfficial(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "plugins"))
	if err != nil {
		t.Fatalf("read plugins dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("..", "..", "plugins", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		p, _, err := Load(string(body), DefaultOptions())
		if err != nil {
			t.Fatalf("%s: Load: %v", e.Name(), err)
		}

		var official map[string]any
		for _, pr := range p.Presets() {
			if pr.ID == "official" {
				official = pr.Opts
			}
		}
		if official == nil {
			t.Logf("%s: no official preset, skipping", e.Name())
			p.Close()
			continue
		}

		plan, err := p.Plan(testCtx(), official)
		if err != nil {
			t.Fatalf("%s: Plan(official): %v", e.Name(), err)
		}
		if plan.Empty() {
			t.Errorf("%s: official preset produced an empty plan", e.Name())
		}
		p.Close()
	}
}
