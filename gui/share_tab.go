package gui

// The sharing tab (#128): what this PC may tell SmartThings (and
// Telegram) about the people using it — the session's lock and idle state
// and who is logged in (smartthings.expose_session / expose_session_user),
// what is playing (media.now_playing, #117) and which watched programs run
// (activity, #110/#123, activity_section.go). All of it is opt-in. The
// switches used to sit in the settings and network tabs.

import (
	"errors"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// --- Pure form model (unit-tested) -----------------------------------------

// shareFormState is the tab's contents as plain values.
type shareFormState struct {
	ExposeSession     bool
	ExposeSessionUser bool
	NowPlaying        bool
	// Activity is the running-app detection toggle and watch list.
	Activity ActivityConfig
}

// shareStateFromConfig is what the tab shows for cfg. The watch list is
// copied so editing it never writes into the baseline.
func shareStateFromConfig(cfg Config) shareFormState {
	return shareFormState{
		ExposeSession:     cfg.SmartThings.ExposeSession,
		ExposeSessionUser: cfg.SmartThings.ExposeSessionUser,
		NowPlaying:        cfg.Media.NowPlaying,
		Activity:          cloneActivity(cfg.Activity),
	}
}

// effectiveUser is the value saved for expose_session_user: the toggle
// keeps its position while session exposure is off (like the telegram
// tab's category masters), but off gates it.
func (s shareFormState) effectiveUser() bool {
	return s.ExposeSession && s.ExposeSessionUser
}

// applyTo returns base with the tab's fields written over it — only those:
// the rest of smartthings and media belong to the SmartThings and settings
// tabs.
func (s shareFormState) applyTo(base Config) Config {
	cfg := base
	cfg.SmartThings.ExposeSession = s.ExposeSession
	cfg.SmartThings.ExposeSessionUser = s.effectiveUser()
	cfg.Media.NowPlaying = s.NowPlaying
	cfg.Activity = normalizeActivity(s.Activity)
	return cfg
}

// dirty reports whether saving would change base.
func (s shareFormState) dirty(base Config) bool {
	return s.ExposeSession != base.SmartThings.ExposeSession ||
		s.effectiveUser() != base.SmartThings.ExposeSessionUser ||
		s.NowPlaying != base.Media.NowPlaying ||
		!activityEqual(s.Activity, base.Activity)
}

// --- Widgets -----------------------------------------------------------------

// shareTab holds the tab's widgets; rebuilt with the window on a language
// change, filled by fillShareTab.
type shareTab struct {
	root        *fyne.Container
	session     *toggle
	sessionUser *toggle
	nowPlaying  *toggle
	// activity is the running-app detection editor (#110).
	activity activityBox
}

// state reads the widgets into the pure form model.
func (t *shareTab) state() shareFormState {
	return shareFormState{
		ExposeSession:     t.session.Checked,
		ExposeSessionUser: t.sessionUser.Checked,
		NowPlaying:        t.nowPlaying.Checked,
		Activity:          t.activity.form(),
	}
}

// setUserEnabled greys the "include user name" toggle while session
// exposure is off.
func (t *shareTab) setUserEnabled(on bool) {
	setEnabled(t.sessionUser, on)
}

func (u *ui) buildShareTab() fyne.CanvasObject {
	t := &shareTab{}
	u.share = t
	onToggle := func(bool) { u.refreshDirty() }
	t.session = newToggle(u.t("st.session"), func(on bool) {
		t.setUserEnabled(on)
		u.refreshDirty()
	})
	t.sessionUser = newToggle(u.t("st.session.user"), onToggle)
	t.sessionUser.Disable()
	t.nowPlaying = newToggle(u.t("settings.nowplaying"), onToggle)
	activity := u.buildActivityBox()

	// The watch list is checked here first so the reason comes in the
	// app's language; the service checks it again.
	ft := u.forms.register(&formTab{
		index: tabShare,
		Fill:  u.fillShareTab,
		Dirty: func(base Config) bool { return t.state().dirty(base) },
		ApplyTo: func(cfg *Config) error {
			s := t.state()
			*cfg = s.applyTo(*cfg)
			if problem := activityProblem(u.lang, s.Activity); problem != "" {
				return errors.New(problem)
			}
			return nil
		},
	})
	ft.bar = newSaveBar(u, func() { u.saveTab(ft) })

	t.root = container.NewVBox(
		hint(u.t("share.intro")),
		section(u.t("share.session"), container.NewVBox(
			t.session,
			hint(u.t("st.session.hint")),
			t.sessionUser,
			hint(u.t("st.session.user.hint")),
		)),
		widget.NewSeparator(),
		section(u.t("share.media"), container.NewVBox(
			t.nowPlaying,
			hint(u.t("settings.nowplaying.hint")),
		)),
		widget.NewSeparator(),
		section(u.t("share.activity"), activity),
		// Trailing padding so the last row never sits flush against the
		// footer (same as the other form tabs).
		widget.NewLabel(""),
	)
	return withSaveBar(t.root, ft.bar)
}

// fillShareTab writes cfg into the tab (the formTab's Fill). UI thread
// only.
func (u *ui) fillShareTab(cfg Config) {
	t := u.share
	if t == nil {
		return
	}
	s := shareStateFromConfig(cfg)
	t.session.SetChecked(s.ExposeSession)
	t.sessionUser.SetChecked(s.ExposeSessionUser)
	t.setUserEnabled(s.ExposeSession)
	t.nowPlaying.SetChecked(s.NowPlaying)
	u.fillActivityBox(s.Activity)
}
