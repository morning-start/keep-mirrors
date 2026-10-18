package engine_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"keep-mirrors/internal/core"
	"keep-mirrors/internal/engine"
)

// A plugin answers for a tool GROUP: pip, uv and pdm each read their own
// setting, and configuring one of them says nothing about the other two. These
// tests pin both halves of the group contract — the members are written, and a
// member that is not installed is left alone.

// fakeTools puts empty executables on PATH so a plugin's presence gate opens
// on a machine that does not have the tool. The plugins only ask "is it on
// PATH", never "what version", so a stub is a faithful stand-in.
func fakeTools(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if runtime.GOOS == "windows" {
			n += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o755); err != nil {
			t.Fatalf("write stub %s: %v", n, err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// presetOpts returns one preset's options by id.
func presetOpts(t *testing.T, p *engine.LuaProvider, id string) map[string]any {
	t.Helper()
	for _, pr := range p.Presets() {
		if pr.ID == id {
			return pr.Opts
		}
	}
	t.Fatalf("%s has no preset %q", p.ID(), id)
	return nil
}

// envChange finds the value a plan would write to name, and whether the name
// appears at all. An unset change reports ("", true): "written empty" and
// "never mentioned" are different outcomes and the tests care about both.
func envChange(plan *core.Plan, name string) (string, bool) {
	for _, c := range plan.Changes {
		if c.Name != name {
			continue
		}
		switch c.Kind {
		case core.ChangeEnv:
			return c.Value, true
		case core.ChangeEnvUnset:
			return "", true
		}
	}
	return "", false
}

// TestSeedPipCarriesUvAndPdm checks the group that makes one preset configure
// every Python installer. pip's index-url reaches pip only, so a preset that
// forgot uv would leave `uv sync` pulling from pypi.org while the badge said
// "已配置".
//
// The gate is part of the contract: a tool that is not installed must not get
// an environment variable nothing reads.
func TestSeedPipCarriesUvAndPdm(t *testing.T) {
	p := loadSeed(t, "pip.lua")
	tuna := presetOpts(t, p, "tuna")
	want := "https://pypi.tuna.tsinghua.edu.cn/simple"

	// An empty PATH is the only honest way to test the negative case on a
	// machine that happens to have uv installed.
	t.Setenv("PATH", t.TempDir())
	plan, err := p.Plan(testCtx(), tuna)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, ok := envChange(plan, "UV_DEFAULT_INDEX"); ok {
		t.Error("UV_DEFAULT_INDEX is written although uv is not on PATH")
	}

	fakeTools(t, "uv", "pdm")
	plan, err = p.Plan(testCtx(), tuna)
	if err != nil {
		t.Fatalf("Plan with uv+pdm present: %v", err)
	}
	for _, name := range []string{"UV_DEFAULT_INDEX", "PDM_PYPI_URL"} {
		got, ok := envChange(plan, name)
		if !ok {
			t.Errorf("%s is not set by the pip preset", name)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	plan, err = p.Plan(testCtx(), presetOpts(t, p, "official"))
	if err != nil {
		t.Fatalf("Plan(official): %v", err)
	}
	for _, name := range []string{"UV_DEFAULT_INDEX", "PDM_PYPI_URL"} {
		if _, ok := envChange(plan, name); !ok {
			t.Errorf("restoring the official source does not clear %s", name)
		}
	}
}

// TestSeedPipReportsHalfAppliedGroup: the badge speaks for pip alone, so a
// group whose members disagree has to say so somewhere. Without this a tool
// installed after the last apply is silently left on pypi.org.
func TestSeedPipReportsHalfAppliedGroup(t *testing.T) {
	p := loadSeed(t, "pip.lua")
	fakeTools(t, "uv", "pdm")
	t.Setenv("PIP_INDEX_URL", "https://mirrors.aliyun.com/pypi/simple/")
	t.Setenv("UV_DEFAULT_INDEX", "")
	t.Setenv("PDM_PYPI_URL", "")

	st := p.Status(testCtx())
	if !strings.Contains(st.Note, "uv") || !strings.Contains(st.Note, "pdm") {
		t.Errorf("note = %q, want it to name both drifted members", st.Note)
	}

	// Aligned again: silence.
	t.Setenv("UV_DEFAULT_INDEX", st.Current)
	t.Setenv("PDM_PYPI_URL", st.Current)
	if st := p.Status(testCtx()); st.Note != "" {
		t.Errorf("note = %q after aligning, want empty", st.Note)
	}
}
