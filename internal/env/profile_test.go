package env

import (
	"strings"
	"testing"
)

// The profile writer is the durable store on POSIX, and cmd/km applies one
// variable per call: the tests below reproduce that sequence, because a
// function that rebuilt the block from the incoming set alone would make the
// second apply silently drop the first variable. That is exactly what happened
// — on Windows the registry hid it, so only a Linux CI run could see it.

var shells = []ShellKind{ShellBash, ShellZsh, ShellFish, ShellPwsh}

// TestUpsertSequentialWritesKeepsBoth is the regression test for the bug:
// apply PUB_HOSTED_URL, then FLUTTER_STORAGE_BASE_URL, and both must survive.
func TestUpsertSequentialWritesKeepsBoth(t *testing.T) {
	for _, sh := range shells {
		t.Run(string(sh), func(t *testing.T) {
			body := UpsertProfile("", sh, map[string]string{
				"PUB_HOSTED_URL": "https://pub.example",
			}, nil)
			body = UpsertProfile(body, sh, map[string]string{
				"FLUTTER_STORAGE_BASE_URL": "https://storage.example",
			}, nil)

			got := ParseProfile(body, sh)
			for name, want := range map[string]string{
				"PUB_HOSTED_URL":           "https://pub.example",
				"FLUTTER_STORAGE_BASE_URL": "https://storage.example",
			} {
				if got[name] != want {
					t.Errorf("%s = %q, want %q (body %q)", name, got[name], want, body)
				}
			}
			if n := strings.Count(body, ShellComment(sh, blockHead)); n != 1 {
				t.Errorf("block head appears %d times, want 1 (body %q)", n, body)
			}
			if n := strings.Count(body, ShellComment(sh, blockTail)); n != 1 {
				t.Errorf("block tail appears %d times, want 1 (body %q)", n, body)
			}
		})
	}
}

// TestUpsertIsIdempotent: writing the same value twice must produce the same
// bytes, or every apply rewrites the profile and the undo diff is noise.
func TestUpsertIsIdempotent(t *testing.T) {
	for _, sh := range shells {
		t.Run(string(sh), func(t *testing.T) {
			set := func() map[string]string {
				return map[string]string{"A_URL": "https://a.example"}
			}
			once := UpsertProfile("", sh, set(), nil)
			twice := UpsertProfile(once, sh, set(), nil)
			if once != twice {
				t.Errorf("%s: not idempotent\nonce:  %q\ntwice: %q", sh, once, twice)
			}
		})
	}
}

// TestUpsertUnsetKeepsSiblings: removing one variable must not remove the other
// one in the same block.
func TestUpsertUnsetKeepsSiblings(t *testing.T) {
	for _, sh := range shells {
		t.Run(string(sh), func(t *testing.T) {
			body := UpsertProfile("", sh, map[string]string{
				"A_URL": "https://a.example",
				"B_URL": "https://b.example",
			}, nil)
			body = UpsertProfile(body, sh, map[string]string{}, []string{"B_URL"})

			got := ParseProfile(body, sh)
			if got["A_URL"] != "https://a.example" {
				t.Errorf("A_URL = %q after removing B_URL (body %q)", got["A_URL"], body)
			}
			if _, ok := got["B_URL"]; ok {
				t.Errorf("B_URL survived its removal (body %q)", body)
			}
		})
	}
}

// TestUpsertRemovingTheLastVariableDropsTheBlock: an empty block is litter the
// user sees in their shell config.
func TestUpsertRemovingTheLastVariableDropsTheBlock(t *testing.T) {
	for _, sh := range shells {
		t.Run(string(sh), func(t *testing.T) {
			body := UpsertProfile("", sh, map[string]string{"A_URL": "https://a.example"}, nil)
			body = UpsertProfile(body, sh, map[string]string{}, []string{"A_URL"})
			if strings.Contains(body, "keep-mirrors") {
				t.Errorf("%s: block left behind: %q", sh, body)
			}
		})
	}
}

// TestUpsertRewritesForeignLineInPlace: the user's own export stays where they
// put it instead of moving into our block.
func TestUpsertRewritesForeignLineInPlace(t *testing.T) {
	sh := ShellBash
	body := "alias ll='ls -l'\nexport PIP_INDEX_URL=\"https://old.example\"\n"
	got := UpsertProfile(body, sh, map[string]string{"PIP_INDEX_URL": "https://new.example"}, nil)

	if !strings.Contains(got, `export PIP_INDEX_URL="https://new.example"`) {
		t.Errorf("foreign line was not kept in place: %q", got)
	}
	if strings.Contains(got, "keep-mirrors") {
		t.Errorf("a variable handled in place also got a block: %q", got)
	}
	if !strings.Contains(got, "alias ll=") {
		t.Errorf("unrelated line was dropped: %q", got)
	}
	if v := ParseProfile(got, sh)["PIP_INDEX_URL"]; v != "https://new.example" {
		t.Errorf("PIP_INDEX_URL = %q, want https://new.example", v)
	}
}

// TestUpsertConvergesDamagedBlock: an older version could nest block markers
// (apply two variables, and the profile ended up with two heads). One pass must
// clean it up, not add a third.
func TestUpsertConvergesDamagedBlock(t *testing.T) {
	sh := ShellBash
	head := ShellComment(sh, blockHead)
	tail := ShellComment(sh, blockTail)
	damaged := "\n" + head + "\n" + head + "\n" +
		`export FLUTTER_STORAGE_BASE_URL="https://storage.example"` + "\n" +
		tail + "\n" + tail

	got := UpsertProfile(damaged, sh, map[string]string{
		"PUB_HOSTED_URL": "https://pub.example",
	}, nil)

	if n := strings.Count(got, head); n != 1 {
		t.Errorf("block head appears %d times after repair, want 1 (body %q)", n, got)
	}
	if n := strings.Count(got, tail); n != 1 {
		t.Errorf("block tail appears %d times after repair, want 1 (body %q)", n, got)
	}
	parsed := ParseProfile(got, sh)
	if parsed["PUB_HOSTED_URL"] != "https://pub.example" {
		t.Errorf("PUB_HOSTED_URL = %q after repair", parsed["PUB_HOSTED_URL"])
	}
	if parsed["FLUTTER_STORAGE_BASE_URL"] != "https://storage.example" {
		t.Errorf("the damaged block's surviving variable was dropped: %q", got)
	}
}

// TestUpsertDoesNotDuplicateAVariable checks the one-variable-one-line rule: a
// variable present both in our block and as a foreign line must end up once.
func TestUpsertDoesNotDuplicateAVariable(t *testing.T) {
	sh := ShellBash
	body := UpsertProfile("", sh, map[string]string{"A_URL": "https://block.example"}, nil)
	body = "export A_URL=\"https://hand.example\"\n" + body

	got := UpsertProfile(body, sh, map[string]string{}, nil)
	if n := strings.Count(got, "A_URL"); n != 1 {
		t.Errorf("A_URL appears %d times, want 1 (body %q)", n, got)
	}
}
