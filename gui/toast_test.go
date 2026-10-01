package gui

import (
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/appid"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// Both toast paths must use the Start menu shortcut's AUMID: under any
// other ID Windows files the toast in the notification center without a
// banner. The grace toast goes through useraction.ShowToast, which shows
// every toast under ToastAppID.
func TestGraceToastUsesShortcutAUMID(t *testing.T) {
	if useraction.ToastAppID != appid.AUMID {
		t.Errorf("toast AppID = %q, want %q", useraction.ToastAppID, appid.AUMID)
	}
}

func TestGraceToastActions(t *testing.T) {
	got := graceToastActions(LangKo)
	if len(got) != 2 || got[0].Arguments != "stpc://runnow" || got[1].Arguments != "stpc://cancel" {
		t.Fatalf("actions = %+v", got)
	}
	for _, a := range got {
		if a.Label == "" {
			t.Errorf("action %q has no label", a.Arguments)
		}
	}
}
