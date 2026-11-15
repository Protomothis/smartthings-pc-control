package ui

// Navigation: the home screen at the bottom of a stack of sub-pages. A
// sub-page has a top bar with a back button; one that edits settings also
// reports unsaved changes, and the save bar shows while it has some.

import (
	"time"

	"gioui.org/layout"
)

// screen is a sub-page.
type screen interface {
	title(u *UI) string
	// body is the scrolling content under the top bar.
	body(gtx C, u *UI, s snapshot) D
}

// editor is a sub-page with a form; the save bar follows dirty.
type editor interface {
	screen
	dirty(s snapshot) bool
	save(u *UI, s snapshot)
	revert(s snapshot)
}

// shower is a sub-page that wants to know when it is opened (to load data
// or to reset its form).
type shower interface {
	shown(u *UI, s snapshot)
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

func (n *nav) push(u *UI, s screen) {
	n.stack = append(n.stack, s)
	n.leaving = time.Time{}
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
	if ed, ok := top.(editor); ok && ed.dirty(u.current()) {
		if now.After(n.leaving) {
			n.leaving = now.Add(leaveArm)
			u.toast.show(u.t("savebar.leave"), false, now)
			return false
		}
		ed.revert(u.current())
	}
	n.stack = n.stack[:len(n.stack)-1]
	n.leaving = time.Time{}
	u.pageList.Position = layout.Position{}
	return true
}
