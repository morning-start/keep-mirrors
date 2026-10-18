package engine_test

import "testing"

// TestSeedCargoCarriesRustup: crates and toolchains come from different hosts,
// so a cargo-only preset leaves `rustup toolchain install` on the slow path.
// The presence gate is rustup, not cargo — the variables belong to rustup.
func TestSeedCargoCarriesRustup(t *testing.T) {
	p := loadSeed(t, "cargo.lua")
	fakeTools(t, "rustup")

	plan, err := p.Plan(testCtx(), presetOpts(t, p, "rsproxy"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got, ok := envChange(plan, "RUSTUP_DIST_SERVER"); !ok || got != "https://rsproxy.cn" {
		t.Errorf("RUSTUP_DIST_SERVER = %q, %v; want https://rsproxy.cn", got, ok)
	}
	if got, ok := envChange(plan, "RUSTUP_UPDATE_ROOT"); !ok || got != "https://rsproxy.cn/rustup" {
		t.Errorf("RUSTUP_UPDATE_ROOT = %q, %v; want https://rsproxy.cn/rustup", got, ok)
	}

	plan, err = p.Plan(testCtx(), presetOpts(t, p, "official"))
	if err != nil {
		t.Fatalf("Plan(official): %v", err)
	}
	for _, name := range []string{"RUSTUP_DIST_SERVER", "RUSTUP_UPDATE_ROOT"} {
		if _, ok := envChange(plan, name); !ok {
			t.Errorf("restoring the official source does not clear %s", name)
		}
	}
}

// TestSeedCargoRustupNeedsRustup pins the gate: a machine with cargo but no
// rustup gets no variables, because nothing would ever read them.
func TestSeedCargoRustupNeedsRustup(t *testing.T) {
	p := loadSeed(t, "cargo.lua")
	t.Setenv("PATH", t.TempDir())

	plan, err := p.Plan(testCtx(), presetOpts(t, p, "rsproxy"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, ok := envChange(plan, "RUSTUP_DIST_SERVER"); ok {
		t.Error("RUSTUP_DIST_SERVER is written although rustup is not on PATH")
	}
}
