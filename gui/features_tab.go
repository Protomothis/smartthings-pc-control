package gui

// The features tab (#157, the sharing tab of #128 before it): every
// opt-in of this PC in one place, one accordion row per feature — its name
// and a short state in the head ("미디어 · 켜짐"), the switches under it.
// All of them start off and all start folded. The switches used to be
// spread over the settings tab (media, PC notifications, monitor, audio
// slots) and the sharing tab (session, now playing, running apps, camera
// and microphone); their config keys did not move, only the tab that saves
// them. One form (featuresFormState), one save bar.

import (
	"errors"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// --- Pure form model (unit-tested) -----------------------------------------

// featuresFormState is the tab's contents as plain values.
type featuresFormState struct {
	// Media is media.enabled (#104): remote volume and media keys.
	Media bool
	// NowPlaying is media.now_playing (#117).
	NowPlaying bool
	// NotifyPC is notify_pc (#106).
	NotifyPC          notifyPCState
	ExposeSession     bool
	ExposeSessionUser bool
	// Activity is the running-app detection toggle and watch list.
	Activity ActivityConfig
	// Privacy is the camera/microphone detection and its guard (#143).
	Privacy PrivacyConfig
	// Monitor is monitor.enabled (#144).
	Monitor bool
	// Audio is the device slots (#145).
	Audio audioSlotsState
}

// featuresStateFromConfig is what the tab shows for cfg. The watch list is
// copied so editing it never writes into the baseline.
func featuresStateFromConfig(cfg Config) featuresFormState {
	return featuresFormState{
		Media:             cfg.Media.Enabled,
		NowPlaying:        cfg.Media.NowPlaying,
		NotifyPC:          notifyPCStateFromConfig(cfg),
		ExposeSession:     cfg.SmartThings.ExposeSession,
		ExposeSessionUser: cfg.SmartThings.ExposeSessionUser,
		Activity:          cloneActivity(cfg.Activity),
		Privacy:           normalizePrivacy(cfg.Privacy),
		Monitor:           cfg.Monitor.Enabled,
		Audio:             audioSlotsFromConfig(cfg),
	}
}

// effectiveUser is the value saved for expose_session_user: the toggle
// keeps its position while session exposure is off (like the telegram
// tab's category masters), but off gates it.
func (s featuresFormState) effectiveUser() bool {
	return s.ExposeSession && s.ExposeSessionUser
}

// applyTo returns base with the tab's fields written over it — only those:
// the rest of smartthings belongs to the SmartThings tab.
func (s featuresFormState) applyTo(base Config) Config {
	cfg := base
	cfg.Media.Enabled = s.Media
	cfg.Media.NowPlaying = s.NowPlaying
	s.NotifyPC.applyTo(&cfg)
	cfg.SmartThings.ExposeSession = s.ExposeSession
	cfg.SmartThings.ExposeSessionUser = s.effectiveUser()
	cfg.Activity = normalizeActivity(s.Activity)
	cfg.Privacy = normalizePrivacy(s.Privacy)
	cfg.Monitor.Enabled = s.Monitor
	s.Audio.applyTo(&cfg)
	return cfg
}

// dirty reports whether saving would change base.
func (s featuresFormState) dirty(base Config) bool {
	return s.Media != base.Media.Enabled ||
		s.NowPlaying != base.Media.NowPlaying ||
		s.NotifyPC.dirty(base) ||
		s.ExposeSession != base.SmartThings.ExposeSession ||
		s.effectiveUser() != base.SmartThings.ExposeSessionUser ||
		!activityEqual(s.Activity, base.Activity) ||
		!privacyEqual(s.Privacy, base.Privacy) ||
		s.Monitor != base.Monitor.Enabled ||
		s.Audio.dirty(base)
}

// The accordion's rows, in order.
const (
	featMedia = iota
	featNotify
	featSession
	featActivity
	featPrivacy
	featMonitor
	featAudio
	featCount
)

// featureTitleKeys are the rows' names, by feat* index.
var featureTitleKeys = [featCount]string{
	featMedia:    "feature.media",
	featNotify:   "feature.notify",
	featSession:  "feature.session",
	featActivity: "feature.activity",
	featPrivacy:  "feature.privacy",
	featMonitor:  "feature.monitor",
	featAudio:    "feature.audio",
}

// featureStatus is the short state after a row's name: on or off, with a
// count where there is one. It follows the switches as they are, saved or
// not.
func featureStatus(l Lang, s featuresFormState, i int) string {
	onOff := func(on bool) string {
		if on {
			return T(l, "feature.on")
		}
		return T(l, "feature.off")
	}
	switch i {
	case featMedia:
		switch {
		case s.Media && s.NowPlaying:
			return T(l, "feature.media.both")
		case s.NowPlaying:
			return T(l, "feature.media.nowplaying")
		}
		return onOff(s.Media)
	case featNotify:
		return onOff(s.NotifyPC.Enabled)
	case featSession:
		return onOff(s.ExposeSession)
	case featActivity:
		if !s.Activity.Enabled {
			return onOff(false)
		}
		return fmt.Sprintf(T(l, "feature.on.count"), len(s.Activity.Watch))
	case featPrivacy:
		return onOff(s.Privacy.Enabled)
	case featMonitor:
		return onOff(s.Monitor)
	case featAudio:
		filled := func(a [audioSlotCount]string) int {
			n := 0
			for _, v := range a {
				if v != "" {
					n++
				}
			}
			return n
		}
		outs, ins := filled(s.Audio.Outputs), filled(s.Audio.Inputs)
		if outs+ins == 0 {
			return T(l, "feature.audio.empty")
		}
		return fmt.Sprintf(T(l, "feature.audio.count"), outs, ins)
	}
	return ""
}

// featureHead is a row's head: "미디어 · 켜짐".
func featureHead(l Lang, s featuresFormState, i int) string {
	return T(l, featureTitleKeys[i]) + " · " + featureStatus(l, s, i)
}

// --- Widgets -----------------------------------------------------------------

// featuresTab holds the tab's widgets; rebuilt with the window on a
// language change, filled by fillFeaturesTab. The PC-notification and
// audio-slot widgets are u.pcNotify and u.audioSlots.
type featuresTab struct {
	root *fyne.Container
	// acc is set last in buildFeaturesTab: the heads are only computed
	// once every widget they read exists.
	acc         *widget.Accordion
	media       *toggle
	nowPlaying  *toggle
	session     *toggle
	sessionUser *toggle
	monitor     *toggle
	// activity is the running-app detection editor (#110).
	activity activityBox
	// privacy is the camera/microphone section (#143).
	privacy *privacyBox
}

// featuresState reads the widgets into the pure form model.
func (u *ui) featuresState() featuresFormState {
	t := u.features
	return featuresFormState{
		Media:             t.media.Checked,
		NowPlaying:        t.nowPlaying.Checked,
		NotifyPC:          u.notifySectionState(),
		ExposeSession:     t.session.Checked,
		ExposeSessionUser: t.sessionUser.Checked,
		Activity:          t.activity.form(),
		Privacy:           t.privacy.form(),
		Monitor:           t.monitor.Checked,
		Audio:             u.audioSlotsFormState(),
	}
}

// setUserEnabled greys the "include user name" toggle while session
// exposure is off.
func (t *featuresTab) setUserEnabled(on bool) {
	setEnabled(t.sessionUser, on)
}

func (u *ui) buildFeaturesTab() fyne.CanvasObject {
	// A language switch rebuilds the tab; the rows that were open stay so.
	var wasOpen []bool
	if old := u.features; old != nil && old.acc != nil {
		for _, it := range old.acc.Items {
			wasOpen = append(wasOpen, it.Open)
		}
	}
	t := &featuresTab{}
	u.features = t
	onToggle := func(bool) { u.refreshDirty() }
	t.media = newToggle(u.t("settings.media"), onToggle)
	t.nowPlaying = newToggle(u.t("settings.nowplaying"), onToggle)
	t.session = newToggle(u.t("st.session"), func(on bool) {
		t.setUserEnabled(on)
		u.refreshDirty()
	})
	t.sessionUser = newToggle(u.t("st.session.user"), onToggle)
	t.sessionUser.Disable()
	t.monitor = newToggle(u.t("settings.monitor"), onToggle)
	notify := u.buildNotifyPCBox()
	activity := u.buildActivityBox()
	var privacy fyne.CanvasObject
	t.privacy, privacy = u.buildPrivacyBox(u.refreshDirty)
	audio := u.buildAudioSlotsSection()

	details := [featCount]fyne.CanvasObject{
		featMedia: container.NewVBox(
			t.media,
			hint(u.t("settings.media.hint")),
			t.nowPlaying,
			hint(u.t("settings.nowplaying.hint")),
		),
		featNotify: notify,
		featSession: container.NewVBox(
			t.session,
			hint(u.t("st.session.hint")),
			t.sessionUser,
			hint(u.t("st.session.user.hint")),
		),
		featActivity: activity,
		featPrivacy:  privacy,
		featMonitor: container.NewVBox(
			t.monitor,
			hint(u.t("settings.monitor.hint")),
		),
		featAudio: audio,
	}

	// The watch list is checked here first so the reason comes in the
	// app's language; the service checks it again.
	ft := u.forms.register(&formTab{
		index: tabFeatures,
		Fill:  u.fillFeaturesTab,
		Dirty: func(base Config) bool { return u.featuresState().dirty(base) },
		ApplyTo: func(cfg *Config) error {
			s := u.featuresState()
			*cfg = s.applyTo(*cfg)
			if problem := activityProblem(u.lang, s.Activity); problem != "" {
				return errors.New(problem)
			}
			return nil
		},
	})
	ft.bar = newSaveBar(u, func() { u.saveTab(ft) })

	items := make([]*widget.AccordionItem, featCount)
	for i, d := range details {
		items[i] = widget.NewAccordionItem(u.t(featureTitleKeys[i]), d)
		items[i].Open = i < len(wasOpen) && wasOpen[i]
	}
	acc := widget.NewAccordion(items...)
	acc.MultiOpen = true

	t.root = container.NewVBox(
		hint(u.t("features.intro")),
		acc,
		// Trailing padding so the last row never sits flush against the
		// footer (same as the other form tabs).
		widget.NewLabel(""),
	)
	t.acc = acc
	u.refreshFeatureHeads()
	return withSaveBar(t.root, ft.bar)
}

// refreshFeatureHeads rewrites the rows' heads from the switches. Called
// from refreshDirty, so every edit (and every fill) lands here. UI thread
// only.
func (u *ui) refreshFeatureHeads() {
	t := u.features
	if t == nil || t.acc == nil {
		return
	}
	s := u.featuresState()
	changed := false
	for i, it := range t.acc.Items {
		if h := featureHead(u.lang, s, i); it.Title != h {
			it.Title = h
			changed = true
		}
	}
	if changed {
		t.acc.Refresh()
	}
}

// fillFeaturesTab writes cfg into the tab (the formTab's Fill). UI thread
// only.
func (u *ui) fillFeaturesTab(cfg Config) {
	t := u.features
	if t == nil {
		return
	}
	s := featuresStateFromConfig(cfg)
	t.media.SetChecked(s.Media)
	t.nowPlaying.SetChecked(s.NowPlaying)
	u.fillNotifySection(cfg)
	t.session.SetChecked(s.ExposeSession)
	t.sessionUser.SetChecked(s.ExposeSessionUser)
	t.setUserEnabled(s.ExposeSession)
	u.fillActivityBox(s.Activity)
	t.privacy.fill(s.Privacy)
	t.monitor.SetChecked(s.Monitor)
	u.fillAudioSlots(cfg)
	go u.loadPrivacy()
}
