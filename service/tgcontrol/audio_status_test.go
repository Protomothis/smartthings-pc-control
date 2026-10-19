package tgcontrol

import (
	"context"
	"strings"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// /status names the output with playing and the input with its mute
// (#159), each part only while it is known.
func TestStatusSpeakerAndMicLines(t *testing.T) {
	f := newMenuControl(t, config.Config{Telegram: config.TelegramConfig{Lang: "ko"}, Media: config.MediaConfig{Enabled: true}})
	vol, muted, playing, micMuted := 30, false, true, true
	speaker, mic := "스피커 <USB>", "마이크"
	f.status.audio = status.Audio{Available: true, Volume: &vol, Muted: &muted, Device: &speaker,
		Playing: &playing, InputDevice: &mic, InputMuted: &micMuted}
	got, _, _ := f.HandleCommand(context.Background(), "42", "status", nil)
	for _, want := range []string{"\n출력: 스피커 &lt;USB&gt; · 재생 중", "\n입력: 마이크 · 음소거"} {
		if !strings.Contains(got, want) {
			t.Errorf("/status lacks %q:\n%s", want, got)
		}
	}

	playing, micMuted = false, false
	if got, _, _ := f.HandleCommand(context.Background(), "42", "status", nil); !strings.Contains(got, "· 소리 없음") || !strings.Contains(got, "입력: 마이크 · 켜짐") {
		t.Errorf("silent and unmuted:\n%s", got)
	}

	// Unknown: no playing part, no input line.
	f.status.audio.Playing, f.status.audio.InputDevice, f.status.audio.InputMuted = nil, nil, nil
	got, _, _ = f.HandleCommand(context.Background(), "42", "status", nil)
	if !strings.Contains(got, "\n출력: 스피커 &lt;USB&gt;\n") && !strings.HasSuffix(got, "\n출력: 스피커 &lt;USB&gt;") {
		t.Errorf("output line while playing is unknown:\n%s", got)
	}
	if strings.Contains(got, "입력:") {
		t.Errorf("input line while unknown:\n%s", got)
	}
}
