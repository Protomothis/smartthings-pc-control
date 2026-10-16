package gui

import (
	"strings"
	"testing"
)

func TestPrivacyFormModel(t *testing.T) {
	// A missing or unknown guard is the service's default, warn.
	if g := normalizePrivacy(PrivacyConfig{}).Guard; g != "warn" {
		t.Errorf("missing guard = %q", g)
	}
	if g := normalizePrivacy(PrivacyConfig{Guard: "nonsense"}).Guard; g != "warn" {
		t.Errorf("unknown guard = %q", g)
	}
	if !privacyEqual(PrivacyConfig{Enabled: true}, PrivacyConfig{Enabled: true, Guard: "warn"}) {
		t.Error("missing guard differs from warn")
	}
	for i, g := range privacyGuards {
		if privacyGuardIndex(g) != i {
			t.Errorf("index of %s = %d", g, privacyGuardIndex(g))
		}
	}

	base := Config{Privacy: PrivacyConfig{Enabled: false, Guard: "warn"}}
	s := shareStateFromConfig(base)
	if s.dirty(base) {
		t.Fatal("fresh state is dirty")
	}
	s.Privacy = PrivacyConfig{Enabled: true, Guard: "block"}
	if !s.dirty(base) {
		t.Fatal("a privacy edit is not dirty")
	}
	if got := s.applyTo(base).Privacy; got != (PrivacyConfig{Enabled: true, Guard: "block"}) {
		t.Errorf("applied %+v", got)
	}
}

func TestPrivacyStateText(t *testing.T) {
	off := privacyStateText(LangKo, PrivacyState{})
	if !strings.Contains(off, T(LangKo, "privacy.state.off")) {
		t.Errorf("off: %q", off)
	}
	on := privacyStateText(LangKo, PrivacyState{Enabled: true, Privacy: &PrivacyBlock{Camera: "in_use", Microphone: "idle"}})
	if on != "지금: 카메라 사용 중 · 마이크 쉬는 중" {
		t.Errorf("on: %q", on)
	}
	en := privacyStateText(LangEn, PrivacyState{Enabled: true, Privacy: &PrivacyBlock{Camera: "unknown", Microphone: "in_use"}})
	if en != "Now: Camera unknown · Microphone in use" {
		t.Errorf("en: %q", en)
	}
}

func TestScheduleInUseText(t *testing.T) {
	if got := scheduleInUseText(LangKo, Schedule{}); got != "" {
		t.Errorf("nothing in use: %q", got)
	}
	got := scheduleInUseText(LangKo, Schedule{InUse: []string{"camera", "microphone"}, Held: true})
	if got != "\n⚠ 카메라·마이크 사용 중\n사용 중이라 한 번 미뤘습니다" {
		t.Errorf("ko: %q", got)
	}
}
