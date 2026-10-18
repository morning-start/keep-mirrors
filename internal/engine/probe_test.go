package engine

import (
	"os"
	"path/filepath"
	"testing"

	"keep-mirrors/internal/paths"
)

// TestProbeReadAndFind isolates the two primitives the plugins rely on for
// status: km.fs.read must return the exact file content, and the Lua pattern
// used to locate the managed block must match it.
func TestProbeReadAndFind(t *testing.T) {
	home := t.TempDir()
	// Registered before the Setenv calls (t.Cleanup is LIFO): without it the
	// package keeps the temp home cached for every later test in this package.
	t.Cleanup(paths.Reset)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	paths.Reset()

	dir := filepath.Join(home, ".config", "go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# >>> keep-mirrors:go >>>\nGOPROXY=https://mirrors.aliyun.com/goproxy,direct\nGOSUMDB=sum.golang.google.cn\n# <<< keep-mirrors:go <<<\n"
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := New(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	probe := `
local M = {}
function M.probe()
	local body = km.fs.read(km.home() .. "/.config/go/env")
	if not body then return "READ_NIL" end
	-- Plain find: '-' is not a Lua pattern metacharacter, and '%-' would be
	-- a lazy quantifier that never matches.
	local s = body:find(">>> keep-mirrors:go >>>", 1, true)
	if not s then return "NO_HEAD at len=" .. #body .. " first40=" .. body:sub(1, 40) end
	local e = body:find("<<< keep-mirrors:go <<<", s, true)
	if not e then return "NO_TAIL" end
	local blk = body:sub(s, e)
	local r = blk:match("GOPROXY%s*=%s*([^\n]+)")
	if not r then return "NO_MATCH blk=" .. blk end
	return "OK:" .. r
end
return M
	`
	if err := st.LoadChunk(probe); err != nil {
		t.Fatal(err)
	}
	res, err := st.CallMethod(st.Module(), "probe")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	got := res[0].String()
	if got != "OK:https://mirrors.aliyun.com/goproxy,direct" {
		t.Errorf("probe = %q", got)
	} else {
		t.Logf("probe = %q", got)
	}
}
