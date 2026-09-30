package gui

import (
	"fmt"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/internal/sapi"
)

// The settings tab's 미디어·알림 section (media-notify doc §4, "UI 구성"):
// the media switches of #104 and #117 next to the PC-notification switches
// of #106 —
// allow, read aloud, voice — and [테스트 알림]. It shares the settings
// tab's save bar: settingsDirty and saveSettings consult notifySection.

// --- Pure model (unit-tested) ------------------------------------------------

// notifyPCState is the PC-notification part of the section as plain values.
type notifyPCState struct {
	Enabled bool
	Speak   bool
	Voice   string // "" = system default
}

func notifyPCStateFromConfig(cfg Config) notifyPCState {
	return notifyPCState{Enabled: cfg.NotifyPC.Enabled, Speak: cfg.NotifyPC.Speak, Voice: cfg.NotifyPC.Voice}
}

// applyTo writes the state into cfg.
func (s notifyPCState) applyTo(cfg *Config) {
	cfg.NotifyPC = NotifyPCConfig{Enabled: s.Enabled, Speak: s.Speak, Voice: strings.TrimSpace(s.Voice)}
}

// dirty reports whether saving would change base.
func (s notifyPCState) dirty(base Config) bool {
	return s.Enabled != base.NotifyPC.Enabled || s.Speak != base.NotifyPC.Speak ||
		strings.TrimSpace(s.Voice) != base.NotifyPC.Voice
}

// voiceOptions builds the voice select: the default entry first (value
// ""), then the installed voices. A configured voice that is not one of
// them exactly (set through the API, or its language pack was removed) is
// kept as an extra entry so saving other fields does not change it.
func voiceOptions(voices []string, current, defaultLabel, missingFmt string) (labels, values []string) {
	labels, values = []string{defaultLabel}, []string{""}
	for _, v := range voices {
		labels = append(labels, v)
		values = append(values, v)
	}
	if c := strings.TrimSpace(current); c != "" && !slices.Contains(voices, c) {
		labels = append(labels, fmt.Sprintf(missingFmt, c))
		values = append(values, c)
	}
	return labels, values
}

// notifyResultKey picks the line shown after a successful test: whether
// the text is being read, and with which voice.
func notifyResultKey(r NotifyResult, speak bool) string {
	switch {
	case !speak:
		return "notifypc.test.shown"
	case !r.Spoken:
		return "notifypc.test.nospeech"
	case r.VoiceFound != nil && !*r.VoiceFound:
		return "notifypc.test.defaultvoice"
	}
	return "notifypc.test.spoken"
}

// --- Widgets -----------------------------------------------------------------

// notifySection holds the section's PC-notification widgets; rebuilt with
// the settings tab on a language change.
type notifySection struct {
	notifyCheck *toggle
	speakCheck  *toggle
	voiceSelect *widget.Select
	// voiceValues[i] is the voice behind option i ("" = system default);
	// voices is the installed list, nil until the COM query returns.
	voiceValues []string
	voices      []string
	voiceHint   *widget.Label
	testBtn     *widget.Button
	testStatus  *widget.Label
}

// buildMediaNotifySection creates the section. head are the settings
// tab's own media switches (media.enabled and media.now_playing with their
// hints), placed first; the PC-notification controls follow.
func (u *ui) buildMediaNotifySection(head ...fyne.CanvasObject) fyne.CanvasObject {
	n := &notifySection{}
	u.pcNotify = n
	onToggle := func(bool) { u.updateSaveState() }
	n.notifyCheck = newToggle(u.t("notifypc.enabled"), onToggle)
	n.speakCheck = newToggle(u.t("notifypc.speak"), func(on bool) {
		n.setSpeakEnabled(on)
		u.updateSaveState()
	})
	n.voiceSelect = widget.NewSelect(nil, func(string) { u.updateSaveState() })
	n.voiceHint = hint(u.t("notifypc.voice.hint"))
	n.testStatus = widget.NewLabel("")
	n.testStatus.Wrapping = fyne.TextWrapWord
	n.testBtn = widget.NewButtonWithIcon(u.t("notifypc.test"), theme.MailSendIcon(), func() { u.sendNotifyTest() })
	u.setVoiceOptions("")
	n.setSpeakEnabled(false)
	u.loadVoices()

	return container.NewVBox(append(head,
		n.notifyCheck,
		hint(u.t("notifypc.hint")),
		n.speakCheck,
		widget.NewForm(widget.NewFormItem(u.t("notifypc.voice"), n.voiceSelect)),
		n.voiceHint,
		container.NewHBox(n.testBtn, layout.NewSpacer()),
		n.testStatus,
	)...)
}

// setSpeakEnabled greys the voice select while speech is off.
func (n *notifySection) setSpeakEnabled(on bool) {
	if on {
		n.voiceSelect.Enable()
	} else {
		n.voiceSelect.Disable()
	}
}

// notifySectionState reads the widgets; the zero state before the section
// exists.
func (u *ui) notifySectionState() notifyPCState {
	n := u.pcNotify
	if n == nil {
		return notifyPCState{}
	}
	s := notifyPCState{Enabled: n.notifyCheck.Checked, Speak: n.speakCheck.Checked}
	if i := n.voiceSelect.SelectedIndex(); i >= 0 && i < len(n.voiceValues) {
		s.Voice = n.voiceValues[i]
	}
	return s
}

// notifySectionDirty reports whether the section differs from base.
func (u *ui) notifySectionDirty(base Config) bool {
	if u.pcNotify == nil {
		return false
	}
	return u.notifySectionState().dirty(base)
}

// fillNotifySection writes cfg into the section. UI thread only; the
// caller re-evaluates the save state.
func (u *ui) fillNotifySection(cfg Config) {
	n := u.pcNotify
	if n == nil {
		return
	}
	s := notifyPCStateFromConfig(cfg)
	n.notifyCheck.SetChecked(s.Enabled)
	n.speakCheck.SetChecked(s.Speak)
	n.setSpeakEnabled(s.Speak)
	u.setVoiceOptions(s.Voice)
	n.testStatus.SetText("")
}

// loadVoices asks SAPI (in this, the user's, session) for the installed
// voices and refreshes the select. The service never lists them: it runs
// in session 0, where the user's voices are not what would speak.
func (u *ui) loadVoices() {
	go func() {
		voices, err := sapi.Voices()
		fyne.Do(func() {
			n := u.pcNotify
			if n == nil {
				return
			}
			if err != nil {
				n.voiceHint.SetText(fmt.Sprintf(u.t("notifypc.voice.error"), err.Error()))
			}
			n.voices = voices
			u.setVoiceOptions(u.notifySectionState().Voice)
			u.updateSaveState()
		})
	}()
}

// setVoiceOptions rebuilds the select around the installed voices with
// current selected. UI thread only.
func (u *ui) setVoiceOptions(current string) {
	n := u.pcNotify
	missing := u.t("notifypc.voice.missing")
	if n.voices == nil {
		missing = "%s" // not listed yet: no reason to call it missing
	}
	labels, values := voiceOptions(n.voices, current, u.t("notifypc.voice.default"), missing)
	n.voiceValues = values
	n.voiceSelect.Options = labels
	n.voiceSelect.SetSelectedIndex(max(0, slices.Index(values, current)))
}

// sendNotifyTest shows a test notification with the section's speech
// settings (saved or not).
func (u *ui) sendNotifyTest() {
	n := u.pcNotify
	s := u.notifySectionState()
	n.testBtn.Disable()
	n.testStatus.Importance = widget.LowImportance
	n.testStatus.SetText(u.t("notifypc.test.sending"))
	go func() {
		res, err := u.client.TestNotify(s.Speak, s.Voice)
		fyne.Do(func() {
			n.testBtn.Enable()
			if err != nil {
				n.testStatus.Importance = widget.DangerImportance
				n.testStatus.SetText(u.actionErrorText(err))
			} else {
				n.testStatus.Importance = widget.SuccessImportance
				text := u.t(notifyResultKey(res, s.Speak))
				if res.VoiceUsed != "" {
					text += " · " + res.VoiceUsed
				}
				if res.SpeakError != "" {
					text += " (" + res.SpeakError + ")"
				}
				n.testStatus.SetText(text)
			}
			n.testStatus.Refresh()
		})
	}()
}

// actionErrorText is err in the user's words when the app knows its code.
func (u *ui) actionErrorText(err error) string {
	if key := actionErrorKey(err); key != "" {
		return u.t(key)
	}
	return err.Error()
}
