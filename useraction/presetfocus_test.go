package useraction

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestValidatePresetFocus(t *testing.T) {
	for raw, want := range map[string]focusTarget{
		"obs64.exe":           {process: "obs64.exe"},
		"OBS64.EXE":           {process: "obs64.exe"},
		"Code - Insiders.exe": {process: "code - insiders.exe"},
		"title:Notepad":       {title: "notepad"},
		"TITLE: Notepad ":     {title: "notepad"},
		"제목:메모장":              {title: "메모장"},
		"제목: - YouTube":       {title: "- youtube"},
		"title:C:\\x":         {title: "c:\\x"},
	} {
		got, err := parsePresetFocus(raw)
		if err != nil || got != want {
			t.Errorf("parsePresetFocus(%q) = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"", "obs64", ".exe", "obs64.EXE ", " obs64.exe", `C:\obs\obs64.exe`, "obs/obs64.exe",
		"title:", "title:   ", "제목:", "notepad", "obs64.exe:x", "window:Notepad",
	} {
		if err := ValidatePresetFocus(raw); !errors.Is(err, ErrPresetFocusInvalid) {
			t.Errorf("ValidatePresetFocus(%q) = %v, want invalid", raw, err)
		}
	}
}

func TestPickWindow(t *testing.T) {
	ws := []windowInfo{
		{hwnd: 1, title: "Untitled - Notepad", process: "notepad.exe"},
		{hwnd: 2, title: "OBS 31.0 - Profile: 방송", process: "obs64.exe", cloaked: true},
		{hwnd: 3, title: "OBS 31.0 - Profile: 게임", process: "obs64.exe"},
		{hwnd: 4, title: "Settings", process: "applicationframehost.exe", cloaked: true},
		{hwnd: 5, title: "메모장 - 제목 없음", process: "notepad.exe"},
	}
	for raw, want := range map[string]windows.HWND{
		"obs64.exe":      3, // the uncloaked one, though behind a cloaked one
		"notepad.exe":    1, // frontmost
		"title:notepad":  1,
		"title:profile:": 3,
		"제목:메모장":         5,
		"title:settings": 4, // only cloaked: still found
		"title:Visual":   0,
		"steam.exe":      0,
	} {
		tgt, err := parsePresetFocus(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := pickWindow(ws, tgt)
		if got != want || ok != (want != 0) {
			t.Errorf("pickWindow(%q) = %v, %v; want %v", raw, got, ok, want)
		}
	}
}

func TestHandlePresetFocus(t *testing.T) {
	lw, bf := listWindows, bringToFront
	t.Cleanup(func() { listWindows, bringToFront = lw, bf })
	listWindows = func() ([]windowInfo, error) {
		return []windowInfo{{hwnd: 7, title: "OBS 31.0 - secret-project", process: "obs64.exe"}}, nil
	}
	var fronted []windows.HWND
	bringToFront = func(h windows.HWND) error { fronted = append(fronted, h); return nil }

	out, err := handlePreset(Request{PresetType: "focus", Path: "obs64.exe"})
	if err != nil || out["started"] != true || len(fronted) != 1 || fronted[0] != 7 {
		t.Fatalf("found: %v, %v, %v", out, err, fronted)
	}

	var ue *Error
	_, err = handlePreset(Request{PresetType: "focus", Path: "title:secret-diary"})
	if !errors.As(err, &ue) || ue.Code != CodeFailed || ue.Reason != PresetWindowNotFound || strings.Contains(ue.Message, "secret") {
		t.Errorf("not found: %+v", err)
	}

	bringToFront = func(windows.HWND) error { return errors.New("SetForegroundWindow was refused (foreground lock)") }
	_, err = handlePreset(Request{PresetType: "focus", Path: "title:secret-project"})
	if !errors.As(err, &ue) || ue.Reason != PresetFocusFailed || strings.Contains(ue.Message, "secret") || ue.Detail == "" {
		t.Errorf("refused: %+v", err)
	}

	listWindows = func() ([]windowInfo, error) { return nil, errors.New("EnumWindows failed") }
	if _, err = handlePreset(Request{PresetType: "focus", Path: "obs64.exe"}); !errors.As(err, &ue) || ue.Reason != PresetFocusFailed {
		t.Errorf("enum failure: %+v", err)
	}
}
