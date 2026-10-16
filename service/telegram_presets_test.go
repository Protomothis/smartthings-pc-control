package service

// Tests for /presets and /run (#109).

import (
	"context"
	"strings"
	"testing"
)

func tgPresetCfg(presets []Preset) Config {
	return Config{Port: 5001, Telegram: TelegramConfig{Lang: "ko", PCName: "PC"}, Presets: presets}
}

func TestTelegramPresets(t *testing.T) {
	initLogger()
	withLiveConfig(t, tgPresetCfg([]Preset{
		{Slot: 2, Name: "<방송>", Type: "script", Path: `C:\s\live.ps1`},
		{Slot: 1, Name: "게임 모드", Type: "program", Path: `C:\Games\steam.exe`, Args: []string{"-secret-flag"}},
	}))
	h := tgCtl
	reply, _, _ := h.HandleCommand(context.Background(), "42", "presets", nil)
	i1, i2 := strings.Index(reply, "1 · 게임 모드"), strings.Index(reply, "2 · &lt;방송&gt;")
	if i1 < 0 || i2 < i1 {
		t.Errorf("list = %q", reply)
	}
	if strings.Contains(reply, "steam.exe") || strings.Contains(reply, "secret-flag") {
		t.Errorf("list shows what a preset runs: %q", reply)
	}

	setConfig(tgPresetCfg(nil))
	if reply, _, _ := h.HandleCommand(context.Background(), "42", "presets", nil); !strings.Contains(reply, "등록된 프리셋이 없습니다") {
		t.Errorf("empty list = %q", reply)
	}
}

func TestTelegramRun(t *testing.T) {
	initLogger()
	withLiveConfig(t, tgPresetCfg(testPresets))
	calls := fakePresetRun(t, `{"ok":true,"started":true}`, nil)
	h := tgCtl

	for _, args := range [][]string{{"3"}, {"게임", "모드"}} {
		reply, _, err := h.HandleCommand(context.Background(), "42", "run", args)
		if err != nil || !strings.Contains(reply, "프리셋 실행: <b>3 · 게임 모드</b>") {
			t.Errorf("/run %q: %q, %v", args, reply, err)
		}
	}
	if len(*calls) != 2 || (*calls)[0][4] != `C:\Games\Steam\steam.exe` {
		t.Errorf("calls = %q", *calls)
	}
	if lr := getLastRemote(); lr.Command != "preset" || lr.Origin != "telegram" || lr.Preset == nil || lr.Preset.Slot != 3 || lr.Preset.Result != "started" {
		t.Errorf("last remote = %+v", lr)
	}

	if reply, _, _ := h.HandleCommand(context.Background(), "42", "run", nil); !strings.Contains(reply, "사용법") {
		t.Errorf("no arg: %q", reply)
	}
	if reply, _, _ := h.HandleCommand(context.Background(), "42", "run", []string{"<x>"}); !strings.Contains(reply, "그런 프리셋이 없습니다: <code>&lt;x&gt;</code>") {
		t.Errorf("unknown: %q", reply)
	}
	if len(*calls) != 2 {
		t.Errorf("unknown preset ran: %q", *calls)
	}

	fakePresetRun(t, "", errNoUserSession)
	if reply, _, _ := h.HandleCommand(context.Background(), "42", "run", []string{"1"}); !strings.Contains(reply, "로그인한 사용자가 없어") {
		t.Errorf("no session: %q", reply)
	}
	if lr := getLastRemote(); lr.Preset == nil || lr.Preset.Result != "no_user_session" {
		t.Errorf("last remote after failure = %+v", lr)
	}
}

// TestTelegramKeysFocusPresets (#146): /presets names the new types, /run
// sends the combination as is, and a focus failure is a fixed text that
// never quotes the program or the title.
func TestTelegramKeysFocusPresets(t *testing.T) {
	initLogger()
	withLiveConfig(t, tgPresetCfg([]Preset{
		{Slot: 1, Name: "마이크", Type: "keys", Path: "ctrl+alt+m"},
		{Slot: 2, Name: "일기", Type: "focus", Path: "제목:secret-diary"},
	}))
	h := tgCtl
	reply, _, _ := h.HandleCommand(context.Background(), "42", "presets", nil)
	if !strings.Contains(reply, "1 · 마이크 <i>(키 입력)</i>") || !strings.Contains(reply, "2 · 일기 <i>(창 앞으로)</i>") ||
		strings.Contains(reply, "ctrl+alt+m") || strings.Contains(reply, "secret") {
		t.Errorf("list = %q", reply)
	}

	calls := fakePresetRun(t, `{"ok":true,"started":true}`, nil)
	if reply, _, err := h.HandleCommand(context.Background(), "42", "run", []string{"1"}); err != nil || !strings.Contains(reply, "1 · 마이크") {
		t.Errorf("/run 1: %q, %v", reply, err)
	}
	if len(*calls) != 1 || strings.Join((*calls)[0], " ") != "preset --type keys --path ctrl+alt+m" {
		t.Errorf("calls = %q", *calls)
	}

	for reason, want := range map[string]string{
		"window_not_found": "열려 있는 창을 찾을 수 없음",
		"focus_failed":     "창을 앞으로 가져오지 못함",
	} {
		fakePresetRun(t, "", &userActionError{Code: "failed", Reason: reason, Message: "x: secret-diary", Detail: "secret-diary"})
		reply, _, err := h.HandleCommand(context.Background(), "42", "run", []string{"2"})
		if err == nil || !strings.Contains(reply, want) || strings.Contains(reply, "secret") {
			t.Errorf("%s: %q (%v), want %q", reason, reply, err, want)
		}
	}
	fakePresetRun(t, "", &userActionError{Code: "failed", Reason: "keys_failed", Message: "could not send the keys"})
	if reply, _, _ := h.HandleCommand(context.Background(), "42", "run", []string{"1"}); !strings.Contains(reply, "키 입력을 보내지 못함") {
		t.Errorf("keys_failed: %q", reply)
	}
}
