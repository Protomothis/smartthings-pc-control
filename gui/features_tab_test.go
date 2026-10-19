package gui

import (
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// findLangSelect is the settings tab's language picker.
func findLangSelect(o fyne.CanvasObject) *widget.Select {
	switch v := o.(type) {
	case *widget.Select:
		if len(v.Options) == 2 && v.Options[0] == "한국어" {
			return v
		}
	case *fyne.Container:
		for _, c := range v.Objects {
			if s := findLangSelect(c); s != nil {
				return s
			}
		}
	}
	return nil
}

// The language picker moved from the top bar to the settings tab's 앱
// section (#157), which stays visible without the service: picking a
// language there rebuilds the window in it and keeps the settings tab.
func TestLanguagePickerOnTheSettingsTab(t *testing.T) {
	u := newTestUI(t, LangKo, nil) // never connected: the settings tab
	sel := findLangSelect(u.settingsRoot)
	if sel == nil {
		t.Fatal("no language picker on the settings tab")
	}
	if !sel.Visible() || u.settingsExtra.Visible() || u.settingsDev.Visible() {
		t.Errorf("offline: picker %v, service settings %v, developer %v", sel.Visible(), u.settingsExtra.Visible(), u.settingsDev.Visible())
	}
	sel.SetSelected("English")
	if u.lang != LangEn || u.tabs.SelectedIndex() != tabSettings || u.tabs.Items[tabFeatures].Text != "Features" {
		t.Errorf("after picking English: lang %s, tab %d, features tab %q", u.lang, u.tabs.SelectedIndex(), u.tabs.Items[tabFeatures].Text)
	}
	if s := findLangSelect(u.settingsRoot); s == nil || s.Selected != "English" {
		t.Error("the rebuilt picker does not show English")
	}
}

// Every row's head is a name and a short state in both languages, never a
// raw locale key, and the state follows the switches.
func TestFeatureHeads(t *testing.T) {
	off := featuresFormState{}
	on := featuresFormState{
		Media: true, NowPlaying: true, NotifyPC: notifyPCState{Enabled: true},
		ExposeSession: true,
		Activity:      ActivityConfig{Enabled: true, Watch: []ActivityWatch{{Slot: 1, Process: "steam.exe"}, {Slot: 2, Process: "obs64.exe"}}},
		Privacy:       PrivacyConfig{Enabled: true, Guard: "warn"},
		Monitor:       true,
		Audio:         audioSlotsState{Outputs: [audioSlotCount]string{"스피커", "", "헤드셋"}, Inputs: [audioSlotCount]string{"마이크"}},
	}
	for _, l := range languages {
		for i := range featCount {
			for _, s := range []featuresFormState{off, on} {
				h := featureHead(l, s, i)
				name, state, ok := strings.Cut(h, " · ")
				if !ok || name != T(l, featureTitleKeys[i]) || state == "" || strings.Contains(h, "feature.") {
					t.Errorf("%s row %d: head %q", l, i, h)
				}
			}
		}
	}
	cases := []struct {
		s    featuresFormState
		row  int
		want string
	}{
		{off, featMedia, "꺼짐"},
		{featuresFormState{Media: true}, featMedia, "켜짐"},
		{featuresFormState{NowPlaying: true}, featMedia, "재생 정보만 공유"},
		{on, featMedia, "켜짐 · 재생 정보 공유"},
		{on, featNotify, "켜짐"},
		{on, featSession, "켜짐"},
		{on, featActivity, "켜짐 · 2개"},
		{featuresFormState{Activity: ActivityConfig{Watch: on.Activity.Watch}}, featActivity, "꺼짐"},
		{on, featPrivacy, "켜짐"},
		{on, featMonitor, "켜짐"},
		{off, featAudio, "비어 있음"},
		{on, featAudio, "출력 2 · 입력 1"},
	}
	for _, c := range cases {
		if got := featureStatus(LangKo, c.s, c.row); got != c.want {
			t.Errorf("row %d of %+v: %q, want %q", c.row, c.s, got, c.want)
		}
	}
}

// The command tab shows a card only for a feature that is on (#157), the
// presets only when there are any, and the line to the features tab while
// something is still off.
func TestCommandCardsShown(t *testing.T) {
	cases := []struct {
		cfg                           *Config
		media, monitor, presets, more bool
	}{
		{nil, false, false, false, false},
		{&Config{}, false, false, false, true},
		{&Config{Media: MediaConfig{Enabled: true}}, true, false, false, true},
		{&Config{Media: MediaConfig{Enabled: true}, Monitor: MonitorConfig{Enabled: true}}, true, true, false, false},
		{&Config{Presets: []Preset{{Slot: 1, Name: "Steam"}}}, false, false, true, true},
	}
	for _, c := range cases {
		media, monitor, presets, more := commandCardsShown(c.cfg)
		if media != c.media || monitor != c.monitor || presets != c.presets || more != c.more {
			t.Errorf("%+v: media %v monitor %v presets %v more %v", c.cfg, media, monitor, presets, more)
		}
	}
}

// The window end to end: the features tab starts folded, its heads follow
// the switches, its switches make only it dirty and save through it, and
// the command tab's cards follow the saved config.
func TestFeaturesTabInTheWindow(t *testing.T) {
	u, svc := newFakeServiceUI(t)
	f := u.features
	if len(f.acc.Items) != featCount {
		t.Fatalf("%d rows, want %d", len(f.acc.Items), featCount)
	}
	for i, it := range f.acc.Items {
		if it.Open {
			t.Errorf("row %d starts open", i)
		}
		if want := featureHead(LangEn, featuresStateFromConfig(*u.forms.base), i); it.Title != want {
			t.Errorf("row %d head %q, want %q", i, it.Title, want)
		}
	}
	// storedConfig: media and monitor off, one preset.
	c := u.cmdCards
	if c.media.Visible() || c.monitor.Visible() || !c.presets.Visible() || !c.more.Visible() {
		t.Errorf("cards with media and monitor off: media %v monitor %v presets %v more %v",
			c.media.Visible(), c.monitor.Visible(), c.presets.Visible(), c.more.Visible())
	}

	f.media.SetChecked(true)
	f.monitor.SetChecked(true)
	if !strings.HasSuffix(f.acc.Items[featMedia].Title, T(LangEn, "feature.on")) {
		t.Errorf("media head after switching on: %q", f.acc.Items[featMedia].Title)
	}
	if d := dirtyIndices(u.forms); !slices.Equal(d, []int{tabFeatures}) {
		t.Fatalf("dirty = %v, want only the features tab", d)
	}
	// Unsaved edits do not move the command tab's cards.
	if c.media.Visible() {
		t.Error("the media card follows an unsaved switch")
	}
	u.saveForms([]*formTab{u.forms.tabAt(tabFeatures)}, true, nil)
	sent := svc.lastPost(t)
	if !sent.Media.Enabled || !sent.Monitor.Enabled || sent.Secret != "s3cret" || sent.Port != 5001 {
		t.Errorf("posted media %v monitor %v secret %q port %d", sent.Media.Enabled, sent.Monitor.Enabled, sent.Secret, sent.Port)
	}
	if !c.media.Visible() || !c.monitor.Visible() || c.more.Visible() {
		t.Errorf("cards after saving: media %v monitor %v more %v", c.media.Visible(), c.monitor.Visible(), c.more.Visible())
	}

	// An open row stays open across a language switch.
	f.acc.Open(featAudio)
	u.lang = LangKo
	u.rebuild()
	if !u.features.acc.Items[featAudio].Open || u.features.acc.Items[featMedia].Open {
		t.Error("the open rows changed with the language")
	}
}
