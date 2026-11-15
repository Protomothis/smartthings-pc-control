package ui

// Navigation: the home screen at the bottom of a stack of sub-pages. A
// sub-page has a top bar with a back button; one that edits settings is a
// form (forms.go), and the save bar shows while it has unsaved changes.

import (
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
)

// screen is a sub-page. Pages are built once (buildPages) and keep their
// state while the app runs; the window being closed and reopened does not
// reset them.
type screen interface {
	title(u *UI) string
	// body is the scrolling content under the top bar (and the page's
	// notice). s is this frame's copy of the service state.
	body(gtx C, u *UI, s snapshot) D
}

// shower is a sub-page that wants to know when it is opened (to load data
// of its own). A form is filled before shown runs.
type shower interface {
	shown(u *UI, s snapshot)
}

// logsWanter is a sub-page that wants the service log polled (every 3s)
// while it is in front; s.logs carries it.
type logsWanter interface {
	wantsLogs() bool
}

// viewportFiller is a sub-page whose body fills the window under the top
// bar instead of growing with its content (the log: its card scrolls,
// the page does not). Its body is laid out exactly as tall as the room
// left under the notice, but never shorter than minBodyHeight; below
// that the page scrolls as usual.
type viewportFiller interface {
	minBodyHeight() unit.Dp
}

// leaveArm is how long a second back press discards unsaved changes.
const leaveArm = 3 * time.Second

// nav is the stack of open sub-pages; empty is the home screen. Window
// goroutine only.
type nav struct {
	stack []screen
	// leaving is set by a back press over unsaved changes: a second one
	// before it runs out leaves without saving.
	leaving time.Time
}

func (n *nav) top() screen {
	if len(n.stack) == 0 {
		return nil
	}
	return n.stack[len(n.stack)-1]
}

// open pushes the registered page id. Window goroutine only.
func (u *UI) open(id pageID) {
	if p := u.pages[id]; p != nil {
		u.nav.push(u, p)
	}
}

func (n *nav) push(u *UI, s screen) {
	n.stack = append(n.stack, s)
	n.leaving = time.Time{}
	if f, ok := s.(form); ok {
		u.formOpened(f)
	}
	if sh, ok := s.(shower); ok {
		sh.shown(u, u.current())
	}
	u.pageList.Position = layout.Position{}
}

// back pops the top page, unless it has unsaved changes and this is the
// first press: then it only warns. It reports whether the page went.
func (n *nav) back(u *UI, now time.Time) bool {
	top := n.top()
	if top == nil {
		return false
	}
	if u.formDirty(top) {
		if now.After(n.leaving) {
			n.leaving = now.Add(leaveArm)
			u.toast.show(u.t("savebar.leave"), false, now)
			return false
		}
		u.revertForm(top.(form))
	}
	u.clearNotice(top)
	n.stack = n.stack[:len(n.stack)-1]
	n.leaving = time.Time{}
	u.pageList.Position = layout.Position{}
	return true
}
