package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"keep-mirrors/internal/paths"
	"keep-mirrors/internal/provider"

	"github.com/yuin/gopher-lua"
)

// luaPlugin is a minimal but complete contract-v1 plugin, used to exercise
// the sandbox and the Lua→Go marshalling without depending on the real
// plugins' contents.
const luaPlugin = `
local M = {}
M.api       = { major = 1, minor = 0 }
M.id        = "test"
M.title     = "Test"
M.category  = "language"
M.platforms = { "windows", "linux" }
M.detects   = { "go" }
M.envs      = { "TEST_INDEX" }
M.presets   = {
  { id = "a", label = "A", opts = { index = "https://example.invalid/a" } },
  { id = "b", label = "B", opts = { index = "" } },
}
M.settings  = {
  { key = "index", type = "string", label = "Index", default = "", validate = "url" },
  { key = "flag",  type = "bool",   label = "Flag",  default = true },
}

function M.status(ctx)
  return {
    installed = km.fs.which("go") ~= nil,
    current   = km.env.get("TEST_INDEX") or "",
    healthy   = true,
    note      = ctx.platform,
  }
end

function M.plan(ctx, opts)
  local index = opts.index or ""
  if index == "" then
    return { changes = { { kind = "env-unset", name = "TEST_INDEX" } } }
  end
  return {
    changes = {
      { kind = "env", name = "TEST_INDEX", value = index },
      { kind = "config", file = "test.conf", format = "hash", syntax = "ini",
        block = "test",
        entries = { { section = "global", key = "index", value = index } } },
    },
    warnings = { "hand-edited block detected" },
  }
end

function M.benchmark(ctx, urls)
  local out = {}
  for _, u in ipairs(urls) do
    table.insert(out, { name = u, url = u, ms = 1, ok = true })
  end
  return out
end

return M
`

func loadTestPlugin(t *testing.T) (*LuaProvider, *State) {
	t.Helper()
	p, st, err := Load(luaPlugin, DefaultOptions())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(p.Close)
	return p, st
}

func TestLoadMetadata(t *testing.T) {
	p, _ := loadTestPlugin(t)

	if got := p.ID(); got != "test" {
		t.Errorf("ID() = %q, want test", got)
	}
	if got := p.Title(); got != "Test" {
		t.Errorf("Title() = %q", got)
	}
	if got := p.Meta().API; got.Major != 1 {
		t.Errorf("Meta().API.Major = %d", got.Major)
	}
	if len(p.Presets()) != 2 {
		t.Errorf("Presets() = %d entries, want 2", len(p.Presets()))
	}
	if len(p.Settings()) != 2 {
		t.Errorf("Settings() = %d entries, want 2", len(p.Settings()))
	}
	if got := p.Settings()[1].Type; got != "bool" {
		t.Errorf("Settings()[1].Type = %q, want bool", got)
	}
}

func TestStatusCallsLua(t *testing.T) {
	p, _ := loadTestPlugin(t)

	st := p.Status(testCtx())
	if !st.Healthy {
		t.Error("Healthy = false, want true")
	}
	if st.Note == "" {
		t.Error("Note is empty; ctx.platform did not reach the plugin")
	}
}

func TestPlanReturnsChanges(t *testing.T) {
	p, _ := loadTestPlugin(t)

	plan, err := p.Plan(testCtx(), map[string]any{
		"index":   "https://example.invalid/a",
		"flag":    true,
		"_preset": "a",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.ChangeCount() != 2 {
		t.Fatalf("ChangeCount() = %d, want 2", plan.ChangeCount())
	}
	if plan.Changes[0].Kind != "env" || plan.Changes[0].Name != "TEST_INDEX" {
		t.Errorf("Changes[0] = %+v", plan.Changes[0])
	}
	if plan.Changes[1].Kind != "config" || len(plan.Changes[1].Entries) != 1 {
		t.Errorf("Changes[1] = %+v", plan.Changes[1])
	}
	if plan.Changes[1].Entries[0].Value != "https://example.invalid/a" {
		t.Errorf("entry value = %q", plan.Changes[1].Entries[0].Value)
	}
	if len(plan.Warnings) != 1 {
		t.Errorf("Warnings = %v, want 1", plan.Warnings)
	}
}

func TestPlanUnsetBranch(t *testing.T) {
	p, _ := loadTestPlugin(t)

	plan, err := p.Plan(testCtx(), map[string]any{"index": ""})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.ChangeCount() != 1 || plan.Changes[0].Kind != "env-unset" {
		t.Fatalf("plan = %s", plan.Summary())
	}
}

func TestBenchmarkCallsLua(t *testing.T) {
	p, _ := loadTestPlugin(t)

	res, err := p.Benchmark(testCtx(), []string{"https://a.invalid", "https://b.invalid"})
	if err != nil {
		t.Fatalf("Benchmark: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("Benchmark returned %d results", len(res))
	}
	if !res[0].OK {
		t.Error("first result is not OK")
	}
}

func testCtx() provider.Context {
	return provider.Context{
		Platform: "windows",
		Home:     os.TempDir(),
		Shell:    "powershell",
		Values:   map[string]any{},
	}
}

// TestSandboxRemovesUnsafeGlobals is the security test: the functions that
// would let a plugin escape must be absent, not merely restricted.
func TestSandboxRemovesUnsafeGlobals(t *testing.T) {
	_, st := loadTestPlugin(t)

	for _, name := range []string{"dofile", "loadfile", "load", "loadstring", "require"} {
		v := st.Global(name)
		if v != nil && v.String() != "nil" {
			t.Errorf("global %q is still reachable: %s", name, v.String())
		}
	}

	// os.execute, os.remove and os.rename must be gone too.
	if osv, ok := st.Global("os").(*lua.LTable); ok {
		for _, name := range []string{"execute", "remove", "rename", "exit", "setenv"} {
			if v := osv.RawGetString(name); v != lua.LNil {
				t.Errorf("os.%q is still reachable: %s", name, v.String())
			}
		}
	}
}

// TestSandboxConfinesFilesystem checks that a read outside home is refused.
// The probe plugin reads a path that cannot be inside the home directory.
func TestSandboxConfinesFilesystem(t *testing.T) {
	outside := filepath.Join(os.TempDir(), "km-escape-probe.txt")

	st, err := New(DefaultOptions())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()

	err = st.LoadChunk(`
		local M = {}
		function M.probe()
			-- A path with no home prefix cannot be resolved inside home.
			return km.fs.read("` + outside + `")
		end
		return M
	`)
	if err != nil {
		t.Fatalf("LoadChunk: %v", err)
	}

	res, err := st.CallMethod(st.Module(), "probe")
	if err == nil && len(res) > 0 && res[0].String() != "nil" {
		t.Fatalf("read outside home succeeded: %s", res[0].String())
	}
	// Either a Lua error (RaiseError) or nil is acceptable; both mean the
	// content did not reach the plugin.
}

// TestSandboxConfinesSymlinkEscape is the security test for the escape that a
// purely lexical containment check misses.
//
// A symlink placed inside home, pointing outside it, has a path that really
// does start with the home directory, so a name-prefix check approves it and
// the subsequent open follows the link out. Only resolving the link catches it.
//
// Creating a symlink needs a privilege Windows does not grant by default, so
// this skips there rather than being deleted.
func TestSandboxConfinesSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()

	// The plugin reads through this link; the test asserts it gets nothing.
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "escape.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	old := paths.Home()
	paths.OverrideHome(home, filepath.Join(home, ".config"))
	defer paths.OverrideHome(old, filepath.Join(old, ".config"))

	st, err := New(DefaultOptions())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()

	// km.home() echoes the sandboxed home, so the plugin builds the link path
	// the same way a real one would: from a path it believes is safe.
	if err := st.LoadChunk(`
		local M = {}
		function M.probe()
			return km.fs.read(km.home() .. "/escape.txt")
		end
		return M
	`); err != nil {
		t.Fatalf("LoadChunk: %v", err)
	}

	res, err := st.CallMethod(st.Module(), "probe")
	if err == nil && len(res) > 0 && res[0].String() == "TOP-SECRET" {
		t.Fatal("a symlink inside home leaked a file from outside it")
	}
}

func TestContextKillsRunawayPlugin(t *testing.T) {
	st, err := New(Options{CallTimeout: 200 * time.Millisecond, MemoryLimitMB: 16})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()

	// The runaway loop lives in the module table, matching the real contract.
	err = st.LoadChunk(`
		local M = {}
		function M.spin()
			local x = 0
			while true do x = x + 1 end
		end
		return M
	`)
	if err != nil {
		t.Fatalf("LoadChunk: %v", err)
	}
	if _, err := st.CallMethod(st.Module(), "spin"); err == nil {
		t.Fatal("runaway plugin was not interrupted")
	}
}

func TestContractMismatchIsRefused(t *testing.T) {
	src := strings.Replace(luaPlugin, "major = 1", "major = 99", 1)
	_, _, err := Load(src, DefaultOptions())
	if err == nil {
		t.Fatal("a plugin targeting contract 99.x was accepted")
	}
	if !strings.Contains(err.Error(), "contract") {
		t.Errorf("error = %v, want a contract mismatch message", err)
	}
}
