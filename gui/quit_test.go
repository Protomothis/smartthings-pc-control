package gui

import (
	"slices"
	"testing"

	"fyne.io/fyne/v2/test"
)

// The tray's [종료] used to quit at once, dropping unsaved edits that
// closing the window asks about (#141). Now it asks the same question, and
// only saving or discarding quits.
func TestTrayQuitAsksAboutUnsavedEdits(t *testing.T) {
	quits := 0
	quit := func() { quits++ }

	u, svc := newFakeServiceUI(t)
	u.requestQuit(quit)
	if quits != 1 || shownDialog(u) != nil {
		t.Fatalf("nothing unsaved: %d quits, dialog %v; want a quit and no question", quits, shownDialog(u))
	}

	quits = 0
	u.win.Hide() // only the tray is on screen
	u.portEntry.SetText("5005")
	u.requestQuit(quit)
	d := shownDialog(u)
	if d == nil {
		t.Fatal("unsaved edits: no question asked")
	}
	if quits != 0 {
		t.Fatal("quit before the question was answered")
	}
	if text := dialogText(d); !slices.Contains(text, T(LangEn, "unsaved.body.quit")) {
		t.Errorf("dialog text = %q", text)
	}
	_, buttons := dialogButtons(d)

	// [계속 편집]: the app keeps running with the edit.
	test.Tap(buttons[T(LangEn, "unsaved.keep")])
	if quits != 0 || u.portEntry.Text != "5005" {
		t.Errorf("keep editing: %d quits, port %q", quits, u.portEntry.Text)
	}

	// [저장]: saved, then quit.
	u.requestQuit(quit)
	_, buttons = dialogButtons(shownDialog(u))
	test.Tap(buttons[T(LangEn, "unsaved.save")])
	if quits != 1 {
		t.Errorf("save: %d quits, want 1", quits)
	}
	if sent := svc.lastPost(t); sent.Port != 5005 {
		t.Errorf("saved port %d, want 5005", sent.Port)
	}

	// [저장 안 함]: quit without posting.
	quits = 0
	posts := len(svc.posts)
	u.portEntry.SetText("5009")
	u.requestQuit(quit)
	_, buttons = dialogButtons(shownDialog(u))
	test.Tap(buttons[T(LangEn, "unsaved.discard")])
	if quits != 1 || len(svc.posts) != posts {
		t.Errorf("discard: %d quits, %d new posts; want a quit and none", quits, len(svc.posts)-posts)
	}
}
