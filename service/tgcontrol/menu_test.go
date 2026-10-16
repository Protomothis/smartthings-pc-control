package tgcontrol

// Tests of the /menu remote, the preset buttons and the /status lines
// added in #147, on recording fakes.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/session"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// menuStatus is fakeStatus with a volume sample and a session.
type menuStatus struct {
	fakeStatus
	audio   status.Audio
	session *session.Info
	idle    int64
	idleOK  bool
}

func (s *menuStatus) Audio(config.Config) status.Audio { return s.audio }
func (s *menuStatus) Session() (session.Info, error) {
	if s.session == nil {
		return session.Info{}, errors.New("nobody")
	}
	return *s.session, nil
}
func (s *menuStatus) IdleSeconds() (int64, bool) { return s.idle, s.idleOK }

// recMedia records the media commands and keeps a mute flag.
type recMedia struct {
	disabled bool
	muted    bool
	ran      []string
}

func (m *recMedia) Run(_ context.Context, name string, _ *int) (session.Result, error) {
	if m.disabled {
		return session.Result{}, action.ErrMediaDisabled
	}
	m.ran = append(m.ran, name)
	switch name {
	case "mute", "unmute":
		m.muted = name == "mute"
		return session.Result{OK: true, Audio: &useraction.Audio{Volume: 30, Muted: m.muted}}, nil
	}
	return session.Result{OK: true}, nil
}
func (m *recMedia) ReadAudio(context.Context) (useraction.Audio, error) {
	if m.disabled {
		return useraction.Audio{}, action.ErrMediaDisabled
	}
	return useraction.Audio{Volume: 30, Muted: m.muted}, nil
}
func (m *recMedia) ReadNowPlaying(context.Context) (useraction.NowPlaying, error) {
	return useraction.NowPlaying{Status: "none"}, nil
}

// recAwake is a keep-awake switch.
type recAwake struct{ on bool }

func (a *recAwake) View() status.AwakeView { return status.AwakeView{On: a.on} }
func (a *recAwake) TurnOn(int) (status.AwakeView, error) {
	a.on = true
	return a.View(), nil
}
func (a *recAwake) TurnOff() (status.AwakeView, bool, error) {
	was := a.on
	a.on = false
	return a.View(), was, nil
}
func (a *recAwake) Now() time.Time { return time.Now() }

// recPresets records the slots run and by whom.
type recPresets struct{ ran []string }

func (p *recPresets) Run(_ context.Context, preset config.Preset, by string) error {
	p.ran = append(p.ran, fmt.Sprintf("%d/%s", preset.Slot, by))
	return nil
}
func (p *recPresets) Record(config.Preset, string, string) {}

type menuFixture struct {
	*testControl
	status  *menuStatus
	media   *recMedia
	awake   *recAwake
	presets *recPresets
}

func newMenuControl(t *testing.T, cfg config.Config) *menuFixture {
	t.Helper()
	f := &menuFixture{testControl: newTestControl(t, cfg), status: &menuStatus{}, media: &recMedia{}, awake: &recAwake{}, presets: &recPresets{}}
	f.d.Status, f.d.Media, f.d.Awake, f.d.Presets = f.status, f.media, f.awake, f.presets
	return f
}

func buttonData(kb *telegram.InlineKeyboard) []string {
	if kb == nil {
		return nil
	}
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.CallbackData)
		}
	}
	return out
}

var twoPresets = []config.Preset{
	{Slot: 2, Name: "<방송>", Type: "script", Path: `C:\s\live.ps1`},
	{Slot: 1, Name: "게임 모드", Type: "program", Path: `C:\Games\steam.exe`},
}

func TestMenuKeyboardFollowsSettings(t *testing.T) {
	f := newMenuControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko"}})
	_, kb, _ := f.HandleCommand(context.Background(), "42", "menu", nil)
	want := []string{"cmd:lock", "cmd:turnscreenoff", "cmd:turnscreenon", "confirm:suspend", "confirm:hibernate",
		"confirm:restart", "confirm:shutdown", "awake:toggle", "cancel:menu"}
	if got := buttonData(kb); !slices.Equal(got, want) {
		t.Errorf("bare menu = %v, want %v", got, want)
	}

	f.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "ko"}, Media: config.MediaConfig{Enabled: true}, Presets: twoPresets})
	_, kb, _ = f.HandleCommand(context.Background(), "42", "menu", nil)
	got := buttonData(kb)
	for _, d := range []string{"media:mute", "media:prev", "media:playpause", "media:next", "menu:presets"} {
		if !slices.Contains(got, d) {
			t.Errorf("full menu lacks %s: %v", d, got)
		}
	}
	// Power commands never run from the menu without a confirmation.
	for _, d := range got {
		for _, power := range []string{"suspend", "hibernate", "restart", "shutdown"} {
			if strings.HasSuffix(d, ":"+power) && !strings.HasPrefix(d, "confirm:") {
				t.Errorf("%s skips the confirmation", d)
			}
		}
	}
}

func TestPresetButtons(t *testing.T) {
	presets := make([]config.Preset, 0, config.PresetMaxSlot)
	for slot := config.PresetMaxSlot; slot >= config.PresetMinSlot; slot-- {
		presets = append(presets, config.Preset{Slot: slot, Name: strings.Repeat("긴이름", 30), Type: "url", Path: "https://example.com"})
	}
	f := newMenuControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko"}, Presets: presets})

	_, kb, _ := f.HandleCommand(context.Background(), "42", "presets", nil)
	data := buttonData(kb)
	if len(data) != config.PresetMaxSlot || data[0] != "run:1" || data[9] != "run:10" {
		t.Errorf("/presets buttons = %v", data)
	}
	sub := buttonData(f.EditKeyboard("menu:presets"))
	if sub[0] != "run:1:menu" || sub[len(sub)-1] != "menu:main" {
		t.Errorf("submenu = %v", sub)
	}
	for _, d := range append(data, sub...) {
		if len(d) > 64 {
			t.Errorf("callback data %q is over Telegram's 64 bytes", d)
		}
	}
	for _, row := range kb.InlineKeyboard {
		if len(row) > 2 {
			t.Errorf("row of %d buttons", len(row))
		}
		for _, b := range row {
			if n := len([]rune(b.Text)); n > 30 {
				t.Errorf("button text %q is %d runes", b.Text, n)
			}
		}
	}

	f.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "ko"}})
	if _, kb, _ := f.HandleCommand(context.Background(), "42", "presets", nil); kb != nil {
		t.Errorf("no presets, buttons %v", buttonData(kb))
	}
}

func TestPresetButtonRunsThroughRun(t *testing.T) {
	f := newMenuControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko", PCName: "PC"}, Presets: twoPresets})
	ctx := context.Background()

	edit, toast, err := f.HandleCallback(ctx, "42", 7, "", "run:2")
	if err != nil || !strings.Contains(edit, "1 · 게임 모드") || !strings.Contains(edit, "▶️ 프리셋 실행: <b>2 · &lt;방송&gt;</b>") {
		t.Errorf("run:2 edit=%q err=%v", edit, err)
	}
	if !strings.HasPrefix(toast, "▶️ 프리셋 실행: 2 · <방송>") {
		t.Errorf("toast = %q", toast)
	}
	if got := buttonData(f.EditKeyboard("run:2")); !slices.Equal(got, []string{"run:1", "run:2"}) {
		t.Errorf("run:2 keeps %v", got)
	}

	edit, _, _ = f.HandleCallback(ctx, "42", 7, "", "run:1:menu")
	if !strings.Contains(edit, "누르면 바로 실행합니다") || !strings.Contains(edit, "1 · 게임 모드</b>") {
		t.Errorf("run:1:menu edit = %q", edit)
	}
	if got := buttonData(f.EditKeyboard("run:1:menu")); got[len(got)-1] != "menu:main" {
		t.Errorf("run:1:menu keeps %v", got)
	}
	if !slices.Equal(f.presets.ran, []string{"2/telegram", "1/telegram"}) {
		t.Errorf("ran = %v", f.presets.ran)
	}

	// A slot that is gone answers like /run; junk is an unknown button.
	if edit, _, err := f.HandleCallback(ctx, "42", 7, "", "run:9"); err == nil || !strings.Contains(edit, "그런 프리셋이 없습니다") {
		t.Errorf("run:9 edit=%q err=%v", edit, err)
	}
	for _, bad := range []string{"run:x", "run:1:other", "run:"} {
		if edit, toast, err := f.HandleCallback(ctx, "42", 7, "", bad); err == nil || edit != "" || toast != "알 수 없는 버튼" {
			t.Errorf("%s: edit=%q toast=%q err=%v", bad, edit, toast, err)
		}
	}
	if len(f.presets.ran) != 2 {
		t.Errorf("a bad button ran a preset: %v", f.presets.ran)
	}
}

func TestMenuSubmenuSwitch(t *testing.T) {
	f := newMenuControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko"}, Presets: twoPresets})
	edit, _, err := f.HandleCallback(context.Background(), "42", 7, "", "menu:presets")
	if err != nil || !strings.Contains(edit, "<b>프리셋</b>") {
		t.Errorf("menu:presets = %q, %v", edit, err)
	}
	edit, _, _ = f.HandleCallback(context.Background(), "42", 7, "", "menu:main")
	if edit != f.menuTitle() {
		t.Errorf("menu:main = %q", edit)
	}
	if got := buttonData(f.EditKeyboard("menu:main")); got[0] != "cmd:lock" {
		t.Errorf("menu:main keyboard = %v", got)
	}
}

func TestMenuAwakeToggle(t *testing.T) {
	f := newMenuControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko"}})
	ctx := context.Background()
	edit, toast, err := f.HandleCallback(ctx, "42", 7, "", "awake:toggle")
	if err != nil || !f.awake.on || !strings.HasPrefix(edit, f.menuTitle()+"\n☕ 잠들지 않기 켜짐") || !strings.HasPrefix(toast, "☕") {
		t.Errorf("on: edit=%q toast=%q err=%v", edit, toast, err)
	}
	edit, _, _ = f.HandleCallback(ctx, "42", 7, "", "awake:toggle")
	if f.awake.on || !strings.Contains(edit, "💤 잠들지 않기 꺼짐") {
		t.Errorf("off: edit=%q", edit)
	}
	if got := buttonData(f.EditKeyboard("awake:toggle")); got[0] != "cmd:lock" {
		t.Errorf("awake keeps %v", got)
	}
}

func TestMenuMediaButtons(t *testing.T) {
	f := newMenuControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko"}, Media: config.MediaConfig{Enabled: true}})
	ctx := context.Background()
	edit, _, err := f.HandleCallback(ctx, "42", 7, "", "media:mute")
	if err != nil || !strings.Contains(edit, "음소거 켜짐") {
		t.Errorf("mute: %q %v", edit, err)
	}
	edit, _, _ = f.HandleCallback(ctx, "42", 7, "", "media:mute")
	if !strings.Contains(edit, "음소거 꺼짐") {
		t.Errorf("unmute: %q", edit)
	}
	for _, key := range []string{"playpause", "prev", "next"} {
		if edit, _, err := f.HandleCallback(ctx, "42", 7, "", "media:"+key); err != nil || !strings.Contains(edit, "키를 보냈습니다") {
			t.Errorf("%s: %q %v", key, edit, err)
		}
	}
	if !slices.Equal(f.media.ran, []string{"mute", "unmute", "playpause", "prev", "next"}) {
		t.Errorf("ran %v", f.media.ran)
	}
	if _, _, err := f.HandleCallback(ctx, "42", 7, "", "media:volume"); err == nil {
		t.Error("media:volume accepted")
	}

	f.media.disabled = true
	edit, toast, err := f.HandleCallback(ctx, "42", 7, "", "media:mute")
	if err != nil || !strings.Contains(edit, "PC 앱 설정 탭(미디어·알림)에서 켜세요") || toast == "" {
		t.Errorf("disabled: edit=%q toast=%q err=%v", edit, toast, err)
	}
}

func TestStatusUptimeVolumeSession(t *testing.T) {
	f := newMenuControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko"}})
	vol, muted := 30, true
	f.status.audio = status.Audio{Available: true, Volume: &vol, Muted: &muted}
	f.status.session = &session.Info{Locked: true}
	f.status.idle, f.status.idleOK = 303, true

	got, _, _ := f.HandleCommand(context.Background(), "42", "status", nil)
	if !strings.Contains(got, "\nPC 가동: 3h 05m\n") {
		t.Errorf("uptime is not the PC's:\n%s", got)
	}
	// Media and session sharing are off: neither line.
	if strings.Contains(got, "볼륨") || strings.Contains(got, "세션") {
		t.Errorf("lines while off:\n%s", got)
	}

	f.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "ko"}, Media: config.MediaConfig{Enabled: true},
		SmartThings: config.SmartThingsConfig{ExposeSession: true}})
	got, _, _ = f.HandleCommand(context.Background(), "42", "status", nil)
	for _, want := range []string{"\n볼륨: 30% · 음소거 켜짐", "\n세션: 잠김 · 유휴 5m 03s"} {
		if !strings.Contains(got, want) {
			t.Errorf("/status lacks %q:\n%s", want, got)
		}
	}

	f.status.audio = status.Audio{}
	f.status.session, f.status.idleOK = nil, false
	f.setConfig(config.Config{Telegram: config.TelegramConfig{Lang: "en"}, Media: config.MediaConfig{Enabled: true},
		SmartThings: config.SmartThingsConfig{ExposeSession: true}})
	got, _, _ = f.HandleCommand(context.Background(), "42", "status", nil)
	if !strings.Contains(got, "\nPC uptime: 3h 05m\n") || !strings.Contains(got, "\nSession: nobody signed in") || strings.Contains(got, "Volume") {
		t.Errorf("en /status:\n%s", got)
	}
	f.status.session = &session.Info{}
	got, _, _ = f.HandleCommand(context.Background(), "42", "status", nil)
	if !strings.HasSuffix(got, "\nSession: in use") {
		t.Errorf("unlocked without heartbeat:\n%s", got)
	}
}

// The "turned off" answers point at the app tab that turns the feature on
// (#147), in both languages.
func TestOffTextsPointAtTheApp(t *testing.T) {
	for _, key := range []string{"media_disabled", "say_disabled"} {
		pair := texts[key]
		if !strings.Contains(pair[0], "PC 앱 설정 탭(미디어·알림)에서 켜세요") {
			t.Errorf("%s ko = %q", key, pair[0])
		}
		if !strings.Contains(pair[1], "PC app's Settings tab (Media &amp; notifications)") {
			t.Errorf("%s en = %q", key, pair[1])
		}
		if strings.Contains(pair[0]+pair[1], "media.enabled") {
			t.Errorf("%s names a config key", key)
		}
	}
	if strings.Contains(texts["notify_off"][0]+texts["notify_off"][1], "파이프라인") {
		t.Error("notify_off still says pipeline")
	}
}
