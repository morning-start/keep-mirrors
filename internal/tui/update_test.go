package tui

import (
	"testing"
	"time"

	"keep-mirrors/internal/app"
	"keep-mirrors/internal/provider"

	tea "github.com/charmbracelet/bubbletea"
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
