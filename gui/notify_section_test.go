package gui

import (
	"reflect"
	"testing"
)

func TestNotifyPCStateRoundTrip(t *testing.T) {
	base := Config{Port: 5001, NotifyPC: NotifyPCConfig{Enabled: true, Speak: true, Voice: "Heami"}, Media: MediaConfig{Enabled: true}}
	s := notifyPCStateFromConfig(base)
	if s.dirty(base) {
		t.Error("an unedited section is dirty")
	}
	s.Voice = " Heami "
	if s.dirty(base) {
		t.Error("surrounding space in the voice is not a change")
	}
	cfg := base
	s.applyTo(&cfg)
	if cfg.NotifyPC.Voice != "Heami" || !cfg.Media.Enabled {
		t.Errorf("applyTo = %+v", cfg)
	}
	s.Speak = false
	if !s.dirty(base) {
		t.Error("speech off is not dirty")
	}
	s = notifyPCStateFromConfig(base)
	s.Enabled = false
	if !s.dirty(base) {
		t.Error("notifications off is not dirty")
	}
}

func TestVoiceOptions(t *testing.T) {
	voices := []string{"Microsoft Heami Desktop - Korean", "Microsoft Zira Desktop - English (United States)"}
	labels, values := voiceOptions(voices, "", "시스템 기본", "%s (없음)")
	if !reflect.DeepEqual(values, append([]string{""}, voices...)) || labels[0] != "시스템 기본" {
		t.Errorf("no current: %q %q", labels, values)
	}
	_, values = voiceOptions(voices, "Microsoft Zira Desktop - English (United States)", "d", "%s (없음)")
	if len(values) != 3 {
		t.Errorf("an installed voice is not appended again: %q", values)
	}
	labels, values = voiceOptions(voices, "Yuna", "d", "%s (없음)")
	if values[len(values)-1] != "Yuna" || labels[len(labels)-1] != "Yuna (없음)" {
		t.Errorf("missing voice: %q %q", labels, values)
	}
	labels, _ = voiceOptions(nil, "Heami", "d", "%s")
	if !reflect.DeepEqual(labels, []string{"d", "Heami"}) {
		t.Errorf("before the list loads: %q", labels)
	}
}

func TestNotifyResultKey(t *testing.T) {
	no, yes := false, true
	for want, c := range map[string]struct {
		r     NotifyResult
		speak bool
	}{
		"notifypc.test.shown":        {NotifyResult{Toast: "shown"}, false},
		"notifypc.test.nospeech":     {NotifyResult{Toast: "shown", SpeakError: "x"}, true},
		"notifypc.test.defaultvoice": {NotifyResult{Spoken: true, VoiceFound: &no}, true},
		"notifypc.test.spoken":       {NotifyResult{Spoken: true, VoiceFound: &yes}, true},
	} {
		if got := notifyResultKey(c.r, c.speak); got != want {
			t.Errorf("%+v: %s, want %s", c, got, want)
		}
		if T(LangKo, want) == want || T(LangEn, want) == want {
			t.Errorf("%s is not translated", want)
		}
	}
}
