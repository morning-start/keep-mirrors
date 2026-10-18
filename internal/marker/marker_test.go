package marker

import (
	"strings"
	"testing"
)

// TestRemoveRoundTrips checks the property that makes "restore default" safe:
// Replace followed by Remove must reproduce the original bytes. A block's
// separator line belongs to the block, not to the file.
func TestRemoveRoundTrips(t *testing.T) {
	cases := []struct{ name, original string }{
		{"no trailing newline", "a = 1\nb = 2"},
		{"trailing newline", "a = 1\nb = 2\n"},
		{"blank line at end", "a = 1\n\n"},
		{"empty file", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := Replace(c.original, Block{Name: "t", Format: Hash, Body: "k = v"})
			back := Remove(rep, "t", Hash)
			// Replace normalises the file to end with exactly one newline (a
			// config file should); Remove cannot and should not undo that.
			// The invariant worth pinning is that no *content* is lost.
			want := c.original
			if want != "" && !strings.HasSuffix(want, "\n") {
				want += "\n"
			}
			if back != want {
				t.Errorf("round trip failed:\n original %q\n replace %q\n removed %q\n want    %q",
					c.original, rep, back, want)
			}
		})
	}
}

// TestRemoveLeavesForeignBlankLines is the regression test for a defect that
// collapsed every blank-line run in the whole file, rewriting parts of the
// file the managed block never touched — violating the package invariant that
// content outside the block is byte-identical.
func TestRemoveLeavesForeignBlankLines(t *testing.T) {
	body := "first\n\n\n\nsecond\n" +
		"# >>> keep-mirrors:t >>>\nk = v\n# <<< keep-mirrors:t <<<\n" +
		"third\n\n\n\nfourth\n"

	out := Remove(body, "t", Hash)

	if !strings.HasPrefix(out, "first\n\n\n\nsecond\n") {
		t.Errorf("blank lines BEFORE the block were collapsed:\n%q", out)
	}
	if !strings.HasSuffix(out, "third\n\n\n\nfourth\n") {
		t.Errorf("blank lines AFTER the block were collapsed:\n%q", out)
	}
	if strings.Contains(out, "keep-mirrors") {
		t.Errorf("block was not removed:\n%q", out)
	}
}

// TestRemoveWithoutBlockIsIdentity checks the no-op path.
func TestRemoveWithoutBlockIsIdentity(t *testing.T) {
	body := "just\ncontent\n"
	if got := Remove(body, "t", Hash); got != body {
		t.Errorf("Remove changed a file with no block: %q -> %q", body, got)
	}
}

// TestRemoveIdempotent checks that removing twice is harmless.
func TestRemoveIdempotent(t *testing.T) {
	rep := Replace("a = 1\n", Block{Name: "t", Format: Hash, Body: "k = v"})
	once := Remove(rep, "t", Hash)
	twice := Remove(once, "t", Hash)
	if once != twice {
		t.Errorf("second Remove changed the result: %q -> %q", once, twice)
	}
}

// TestReplaceAndPresent checks the helpers the diff path relies on.
func TestReplaceAndPresent(t *testing.T) {
	original := "a = 1\n"
	rep := Replace(original, Block{Name: "t", Format: Hash, Body: "k = v"})
	if !Present(rep, Block{Name: "t", Format: Hash, Body: "k = v"}) {
		t.Error("Present did not recognise the block Replace just wrote")
	}
	if Present(rep, Block{Name: "t", Format: Hash, Body: "k = other"}) {
		t.Error("Present accepted a different body")
	}
}
