package plugins

import (
	"io/fs"
	"strings"
	"testing"
)

// TestProvenanceIsStamped checks the snapshot carries a usable commit hash.
//
// An unstamped snapshot still runs, but it cannot answer "is this up to date",
// so every install would re-download the plugin set on first sync. Failing here
// catches a release that was assembled without running the stamp step.
func TestProvenanceIsStamped(t *testing.T) {
	p := Snapshot()
	if !p.Valid() {
		t.Fatalf("PROVENANCE carries no usable SHA (got %q); run the stamp command before releasing", p.SHA)
	}
	if p.Source == "" {
		t.Error("PROVENANCE does not record the source repository")
	}
	if p.Branch == "" {
		t.Error("PROVENANCE does not record the branch")
	}
}

// TestProvenanceFormat checks the SHA is exactly what git emits: 40 lowercase
// hex characters. Anything else would compare unequal to a real commit and
// trigger a needless sync on every run.
func TestProvenanceFormat(t *testing.T) {
	p := Snapshot()
	if len(p.SHA) != 40 {
		t.Fatalf("SHA has %d characters, want 40", len(p.SHA))
	}
	if p.SHA != strings.ToLower(p.SHA) {
		t.Error("SHA is not lowercase; git prints lowercase and comparison is exact")
	}
	for _, r := range p.SHA {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("SHA contains %q, which is not hex", r)
		}
	}
}

// TestProvenanceIsNotAPluginFile guards a specific mistake: PROVENANCE lives in
// the same embedded directory as the plugins, so anything that enumerates the
// snapshot must filter by extension. If this file were ever treated as a
// plugin, loading would fail with a confusing Lua error.
func TestProvenanceIsNotAPluginFile(t *testing.T) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	var lua int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lua") {
			lua++
			continue
		}
		if e.Name() != "PROVENANCE" {
			t.Errorf("unexpected non-plugin file %q in the snapshot", e.Name())
		}
	}
	if lua == 0 {
		t.Fatal("the snapshot holds no plugins")
	}
}

// TestParseProvenance covers the parser directly, including the shapes a
// hand-edited file can take.
//
// The parser does not judge validity — it keeps whatever it read so a broken
// file can be diagnosed ("it says abc123") rather than silently looking empty.
// Valid() is what decides whether the value is usable.
func TestParseProvenance(t *testing.T) {
	const sha = "784478f187ab58e139c65d250e2ced1e5273f375"

	cases := []struct {
		name  string
		body  string
		want  string
		valid bool
	}{
		{"plain", "sha=" + sha + "\n", sha, true},
		{"crlf", "sha=" + sha + "\r\nsource=x\r\n", sha, true},
		{"uppercase normalised", "sha=" + strings.ToUpper(sha) + "\n", sha, true},
		{"quoted", `sha="` + sha + `"` + "\n", sha, true},
		{"comments ignored", "# built by ci\nsha=" + sha + "\n", sha, true},
		{"extra keys ignored", "sha=" + sha + "\ndate=2026-01-01\n", sha, true},
		{"empty", "", "", false},
		{"garbage", "not a key value file\n", "", false},
		{"short kept but invalid", "sha=abc123\n", "abc123", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := parseProvenance(c.body)
			if p.SHA != c.want {
				t.Errorf("SHA = %q, want %q", p.SHA, c.want)
			}
			if p.Valid() != c.valid {
				t.Errorf("Valid() = %v, want %v", p.Valid(), c.valid)
			}
		})
	}
}

// TestProvenanceShort checks the display form, which must never panic on an
// unstamped snapshot.
func TestProvenanceShort(t *testing.T) {
	p := Provenance{SHA: "784478f187ab58e139c65d250e2ced1e5273f375"}
	if got := p.Short(); got != "784478f" {
		t.Errorf("Short() = %q", got)
	}
	if got := (Provenance{}).Short(); got != "unstamped" {
		t.Errorf("Short() on empty = %q, want unstamped", got)
	}
}
