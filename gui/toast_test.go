package gui

import (
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/appid"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// Both toast paths must use the Start menu shortcut's AUMID: under any
// other ID Windows files the toast in the notification center without a
// banner.
func TestGraceToastUsesShortcutAUMID(t *testing.T) {
	n := graceToast(LangKo, "종료 예정", "30초 뒤 종료")
	if n.AppID != appid.AUMID {
		t.Errorf("grace toast AppID = %q, want %q", n.AppID, appid.AUMID)
	}
	if n.AppID != useraction.ToastAppID {
		t.Errorf("grace toast AppID %q differs from the PC notification's %q", n.AppID, useraction.ToastAppID)
	}
	if len(n.Actions) != 2 {
		t.Errorf("actions = %+v", n.Actions)
	}
}
