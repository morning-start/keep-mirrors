package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/env"
	"keep-mirrors/internal/provider"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestInitArmsThePollTimer checks that hot reload starts at all: without this
// the ticker never fires and the only symptom is "edits are not picked up".
func TestInitArmsThePollTimer(t *testing.T) {
	m := newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})

	withPoll := New(m, func() app.ReloadedMsg { return app.ReloadedMsg{} })
	if withPoll.Init() == nil {
		t.Error("Init returned no command with a poll function set")
	}

	headless := New(m, nil)
	if headless.Init() != nil {
		t.Error("Init armed a timer without a poll function")
	}
}

// TestTickPollsAndRearms is the invariant that keeps polling alive: the handler
// must both run the poll and schedule the next tick. Returning only the poll
// would stop watching after one second, which looks exactly like a broken
// watcher and is invisible in normal use.
func TestTickPollsAndRearms(t *testing.T) {
	polled := 0
	poll := func() app.ReloadedMsg {
		polled++
		return app.ReloadedMsg{}
	}
	m := Model{app: newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage}), poll: poll}

	next, cmd := m.Update(tickMsg{})
	if cmd == nil {
		t.Fatal("handling a tick returned no command; polling would stop")
	}

	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	if nm.poll == nil {
		t.Error("the poll function was dropped from the model")
	}

	// The batch contains the poll command; run it and confirm it fired.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("tick produced %T, want a batch of (poll, next tick)", cmd())
	}
	if len(batch) != 2 {
		t.Fatalf("batch has %d commands, want the poll and the next tick", len(batch))
	}
	batch[0]()
	if polled != 1 {
		t.Errorf("poll ran %d times, want 1", polled)
	}
}

// TestTickWithoutPollIsInert makes sure a headless model does not spin a timer.
func TestTickWithoutPollIsInert(t *testing.T) {
	m := Model{app: newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})}

	_, cmd := m.Update(tickMsg{})
	if cmd != nil {
		t.Error("a model with no poll function returned a command")
	}
}

// TestReloadedMsgReachesTheModel checks the plumbing end to end: the message
// returned by the poll function must actually update application state.
func TestReloadedMsgReachesTheModel(t *testing.T) {
	p := &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage}
	am := newModel(t, p)
	m := New(am, nil)

	// Simulate the message the poll would deliver.
	fresh := &stub{id: "npm", title: "Npm", cat: provider.CategoryLanguage}
	next, cmd := m.Update(app.ReloadedMsg{
		Changed:   true,
		Providers: []provider.Provider{fresh},
	})
	if cmd != nil {
		t.Errorf("handling a reload returned a command, want none")
	}
	if _, ok := am.Reg.Get("npm"); !ok {
		t.Error("the reload message did not reach the application model")
	}
	_ = next
}

// TestArrowKeysMoveTheCursor is the regression test for an interface that only
// answered to vim bindings. A first-time user presses the arrow keys; if
// nothing happens they have to open the help screen to discover that "j" is
// what they wanted. The arrow keys and the vim aliases must both move the
// cursor to the same place.
func TestArrowKeysMoveTheCursor(t *testing.T) {
	cases := []struct {
		name    string
		downKey tea.KeyType
		downStr string
		upKey   tea.KeyType
		upStr   string
	}{
		{"arrows", tea.KeyDown, "", tea.KeyUp, ""},
		{"vim aliases", 0, "j", 0, "k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			am := newModel(t,
				&stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage},
				&stub{id: "npm", title: "Npm", cat: provider.CategoryLanguage},
			)
			m := New(am, nil)
			if am.Cursor != 0 {
				t.Fatalf("cursor starts at %d, want 0", am.Cursor)
			}

			next, _ := m.Update(keyMsg(tc.downKey, tc.downStr))
			nm := next.(Model)
			if nm.app.Cursor != 1 {
				t.Errorf("cursor = %d after the down key, want 1", nm.app.Cursor)
			}

			next, _ = nm.Update(keyMsg(tc.upKey, tc.upStr))
			nm = next.(Model)
			if nm.app.Cursor != 0 {
				t.Errorf("cursor = %d after the up key, want 0", nm.app.Cursor)
			}
		})
	}
}

// TestHomeEndJumpToEnds covers the "go to top/bottom" keys, which used to be
// vim-only (g/G) and unreachable for everyone else.
func TestHomeEndJumpToEnds(t *testing.T) {
	am := newModel(t,
		&stub{id: "a", title: "A", cat: provider.CategoryLanguage},
		&stub{id: "b", title: "B", cat: provider.CategoryLanguage},
		&stub{id: "c", title: "C", cat: provider.CategoryLanguage},
	)
	m := New(am, nil)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	nm := next.(Model)
	if nm.app.Cursor != 2 {
		t.Errorf("cursor = %d after End, want the last row (2)", nm.app.Cursor)
	}

	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyHome})
	nm = next.(Model)
	if nm.app.Cursor != 0 {
		t.Errorf("cursor = %d after Home, want 0", nm.app.Cursor)
	}
}

// TestPageKeysMoveByAPage keeps PgUp/PgDn from being dead keys and from
// overshooting: they travel a bounded number of rows, then clamp at the end
// like every other movement.
func TestPageKeysMoveByAPage(t *testing.T) {
	t.Run("longer than a page", func(t *testing.T) {
		var stubs []provider.Provider
		for i := 0; i < pageSize*2; i++ {
			id := fmt.Sprintf("p%02d", i)
			stubs = append(stubs, &stub{id: id, title: id, cat: provider.CategoryLanguage})
		}
		am := newModel(t, stubs...)
		m := New(am, nil)

		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		nm := next.(Model)
		if nm.app.Cursor != pageSize {
			t.Errorf("cursor = %d after PgDn, want %d", nm.app.Cursor, pageSize)
		}

		next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		nm = next.(Model)
		if nm.app.Cursor != 0 {
			t.Errorf("cursor = %d after PgUp, want 0", nm.app.Cursor)
		}
	})

	t.Run("shorter than a page", func(t *testing.T) {
		var stubs []provider.Provider
		for _, id := range []string{"a", "b", "c", "d"} {
			stubs = append(stubs, &stub{id: id, title: id, cat: provider.CategoryLanguage})
		}
		am := newModel(t, stubs...)
		m := New(am, nil)

		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		nm := next.(Model)
		if nm.app.Cursor != 3 {
			t.Errorf("cursor = %d after PgDn on a 4-row list, want it clamped to 3", nm.app.Cursor)
		}
	})
}

// TestLeftRightCyclePresets checks that the horizontal arrows (and the p
// binding) all advance the preset, and that cycling wraps in both directions
// instead of dead-ending on the first or last entry.
func TestLeftRightCyclePresets(t *testing.T) {
	am := newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})
	m := New(am, nil)

	// The stub declares tuna then official, and tuna is the default.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	nm := next.(Model)
	if got := nm.app.Presets["pip"]; got != "official" {
		t.Fatalf("preset after right = %q, want official", got)
	}

	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyRight})
	nm = next.(Model)
	if got := nm.app.Presets["pip"]; got != "tuna" {
		t.Errorf("preset wrapped to %q, want tuna (right at the end should wrap)", got)
	}

	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyLeft})
	nm = next.(Model)
	if got := nm.app.Presets["pip"]; got != "official" {
		t.Errorf("preset after left = %q, want official (left at the start should wrap)", got)
	}
}

// cleanTestEnv clears TEST_INDEX (persisted and process) and restores whatever
// was there when the test ends.
//
// The stub plan writes this variable, and Windows cannot sandbox HKCU, so a
// test that dies between write and undo would leak the value into every later
// run — where Before == After makes the plan empty and the flow untestable.
// Starting clean breaks that cascade; the cleanup keeps the machine whole.
func cleanTestEnv(t *testing.T) {
	t.Helper()
	saved, _ := env.Read("TEST_INDEX")
	proc, hadProc := os.LookupEnv("TEST_INDEX")
	t.Cleanup(func() {
		if saved == "" {
			_ = env.Delete("TEST_INDEX")
		} else {
			_ = env.Write("TEST_INDEX", saved)
		}
		if hadProc {
			os.Setenv("TEST_INDEX", proc)
		} else {
			os.Unsetenv("TEST_INDEX")
		}
	})
	_ = env.Delete("TEST_INDEX")
}

// TestEnterStagesThenConfirmApplies pins the three-step apply: the first
// Enter stages (opens the confirmation panel, writes nothing), a second Enter
// confirms, and Ctrl+Z still rolls the confirmed write back.
//
// Before staging existed the first Enter wrote to disk directly — one
// accidental keypress on the keyboard next to the navigation keys was enough
// to modify the user's configuration.
func TestEnterStagesThenConfirmApplies(t *testing.T) {
	am := newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})
	cleanTestEnv(t)
	m := New(am, nil)

	// 1. First Enter: stage only. The variable must still be unset.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(Model)
	if !nm.confirming {
		t.Fatal("the first Enter did not open the confirmation panel")
	}
	if os.Getenv("TEST_INDEX") != "" {
		t.Fatal("the first Enter already wrote the configuration")
	}

	// 2. While staged, navigation keys are swallowed: the diff on screen must
	// not change out from under the Enter that will confirm it.
	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyDown})
	nm = next.(Model)
	if !nm.confirming {
		t.Fatal("a navigation key dismissed the confirmation panel")
	}
	if am.Cursor != 0 {
		t.Error("a navigation key moved the cursor while staged")
	}

	// 3. Second Enter: confirm and write.
	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm = next.(Model)
	if nm.confirming {
		t.Error("confirming stayed on after the write")
	}
	if os.Getenv("TEST_INDEX") == "" {
		t.Error("the confirmed Enter did not write the configuration")
	}
	if !strings.HasPrefix(nm.app.StatusMsg, "applied") {
		t.Errorf("status after confirm = %q, want an applied message", nm.app.StatusMsg)
	}

	// 4. Ctrl+Z still rolls it back.
	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
	nm = next.(Model)
	if !strings.HasPrefix(nm.app.StatusMsg, "restored") {
		t.Errorf("status after ctrl+z = %q, want a restored message", nm.app.StatusMsg)
	}
}

// TestEscCancelsStagedPlan checks the way out: cancelling leaves the plan
// pending but writes nothing, and says so in plain words rather than an error.
func TestEscCancelsStagedPlan(t *testing.T) {
	am := newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})
	cleanTestEnv(t)
	m := New(am, nil)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(Model)

	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	nm = next.(Model)
	if nm.confirming {
		t.Error("esc did not leave the confirmation panel")
	}
	if os.Getenv("TEST_INDEX") != "" {
		t.Error("esc wrote the configuration")
	}
	if !strings.Contains(nm.app.StatusMsg, "已取消") {
		t.Errorf("status after esc = %q, want a cancellation notice", nm.app.StatusMsg)
	}
}

// TestOKeyStagesOfficialRevert checks the dedicated way back: o selects the
// official preset and stages it, so reverting is one deliberate keypress plus
// one confirmation instead of cycling the picker.
func TestOKeyStagesOfficialRevert(t *testing.T) {
	am := newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})
	cleanTestEnv(t)
	m := New(am, nil)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	nm := next.(Model)
	if got := nm.app.Presets["pip"]; got != "official" {
		t.Fatalf("preset after o = %q, want official", got)
	}
	if !nm.confirming {
		t.Fatal("o did not stage the official plan")
	}

	// Cancelling leaves the official preset selected but nothing written.
	next, _ = nm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	nm = next.(Model)
	if nm.app.Presets["pip"] != "official" {
		t.Error("cancelling reset the preset selection")
	}
}

// TestPresetNoteShown checks the official default is named where the user is
// looking: the preset picker renders each preset's note.
func TestPresetNoteShown(t *testing.T) {
	am := newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})
	m := New(am, nil)

	if out := ansi.Strip(View(m.app, false, false, "")); !strings.Contains(out, "pypi.org") {
		t.Errorf("the official preset's note is not shown:\n%s", out)
	}
}

// TestFilterAcceptsMultibyteInput is the regression test for a filter that only
// accepted single-byte characters: Chinese is three bytes per character, so a
// len(s)==1 test dropped every keystroke and the prompt looked frozen.
func TestFilterAcceptsMultibyteInput(t *testing.T) {
	am := newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})
	m := New(am, nil)
	m.filtering = true

	for _, ch := range []string{"p", "y", "通"} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(ch)})
		m = next.(Model)
	}
	if m.filterDraft != "py通" {
		t.Errorf("filter draft = %q, want %q", m.filterDraft, "py通")
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	if m.filterDraft != "py" {
		t.Errorf("draft after backspace = %q, want %q", m.filterDraft, "py")
	}

	// A control key must not insert its escape sequence.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(Model)
	if m.filterDraft != "py" {
		t.Errorf("draft = %q after an arrow key, want it unchanged", m.filterDraft)
	}
}

// keyMsg builds the key message a real terminal sends. bubbletea reports
// arrows and friends by KeyType with an empty string, and letters by runes.
func keyMsg(t tea.KeyType, s string) tea.KeyMsg {
	if s != "" {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	return tea.KeyMsg{Type: t}
}

// TestHelpLeadsWithArrowKeys guards the documentation half of the same problem:
// the help screen is where a confused user goes, so it must present the
// canonical keys, not advertise j/k as the interface.
func TestHelpLeadsWithArrowKeys(t *testing.T) {
	help := renderHelp()
	for _, want := range []string{"↓", "↑", "Enter", "Ctrl+Z"} {
		if !strings.Contains(help, want) {
			t.Errorf("help screen does not mention %q", want)
		}
	}
	if strings.Contains(help, "  j  ") {
		t.Error("help screen still presents j as a primary key")
	}
}

// TestCtrlCQuitsWhileFiltering is the regression test for a key that could trap
// the user: the filter prompt swallows every keystroke, so without an explicit
// case for it, ctrl+c does nothing once the prompt is open.
func TestCtrlCQuitsWhileFiltering(t *testing.T) {
	am := newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})
	m := New(am, nil)
	m.filtering = true

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c while filtering returned no quit command")
	}
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	if !nm.app.Quit {
		t.Error("ctrl+c while filtering did not set Quit")
	}
}

// TestFilterEnterStillApplies keeps the ctrl+c case from breaking the normal
// path: enter must commit the draft rather than close the prompt.
func TestFilterEnterStillApplies(t *testing.T) {
	am := newModel(t, &stub{id: "pip", title: "Pip", cat: provider.CategoryLanguage})
	m := New(am, nil)
	m.filtering = true
	m.filterDraft = "pip"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	if nm.filtering {
		t.Error("enter did not close the filter prompt")
	}
	if nm.app.Filter != "pip" {
		t.Errorf("filter = %q, want the committed draft", nm.app.Filter)
	}
	if nm.app.Quit {
		t.Error("enter while filtering quit the program")
	}
}

// TestTickCommandDeliversTickMsg documents the wiring between tickCmd and the
// tickMsg the handler switches on. A mismatch would make the timer fire into a
// type nobody handles.
func TestTickCommandDeliversTickMsg(t *testing.T) {
	msg := tickCmd(1 * time.Millisecond)()
	if _, ok := msg.(tickMsg); !ok {
		t.Fatalf("tickCmd produced %T, want tickMsg", msg)
	}
}
