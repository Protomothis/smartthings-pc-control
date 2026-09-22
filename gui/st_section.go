package gui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The SmartThings section of the network tab (issue #70, edge-driver doc
// §7): the Edge driver's connection state plus the `smartthings` part of
// the config (§3.7) — SSDP discovery, session exposure and the hub allow
// list. Saving goes through the shared saveBar flow like the settings and
// notifications tabs; only the smartthings fields are written over the
// baseline, so a save here never clobbers another tab's edits.

// --- Pure form model (unit-tested) -----------------------------------------

// stFormState is the section's contents as plain values, independent of
// widgets, so the config round-trip and the dirty check are testable.
type stFormState struct {
	Discovery         bool
	ExposeSession     bool
	ExposeSessionUser bool
	// Hubs is the edited allow list; empty means "any hub".
	Hubs []string
}

// stStateFromConfig is what the section shows for cfg. The hub list is
// copied so editing it never writes into the baseline.
func stStateFromConfig(cfg Config) stFormState {
	return stFormState{
		Discovery:         cfg.SmartThings.Discovery,
		ExposeSession:     cfg.SmartThings.ExposeSession,
		ExposeSessionUser: cfg.SmartThings.ExposeSessionUser,
		Hubs:              normalizeHubs(cfg.SmartThings.AllowedHubs),
	}
}

// effectiveUser is the value saved for expose_session_user: the toggle
// keeps its position while session exposure is off (like the notify tab's
// category masters), but off gates it.
func (s stFormState) effectiveUser() bool {
	return s.ExposeSession && s.ExposeSessionUser
}

// normalizeHubs trims, drops blanks and de-duplicates the allow list.
// Never nil: the service reads a missing/null allowed_hubs as "keep the
// stored list", so clearing it has to send [].
func normalizeHubs(hubs []string) []string {
	out := []string{}
	for _, h := range hubs {
		if h = strings.TrimSpace(h); h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// addHub returns the list with ip appended when it is not already on it.
func addHub(hubs []string, ip string) []string {
	out := normalizeHubs(hubs)
	ip = strings.TrimSpace(ip)
	if ip == "" || slices.Contains(out, ip) {
		return out
	}
	return append(out, ip)
}

// removeHub returns the list without ip.
func removeHub(hubs []string, ip string) []string {
	out := []string{}
	for _, h := range normalizeHubs(hubs) {
		if h != ip {
			out = append(out, h)
		}
	}
	return out
}

// applyTo returns base with the section's fields written over smartthings.
// Everything else (port, secret, telegram, notify…) is untouched.
func (s stFormState) applyTo(base Config) Config {
	cfg := base
	cfg.SmartThings = SmartThingsConfig{
		Discovery:         s.Discovery,
		AllowedHubs:       normalizeHubs(s.Hubs),
		ExposeSession:     s.ExposeSession,
		ExposeSessionUser: s.effectiveUser(),
	}
	return cfg
}

// dirty reports whether saving would change base.
func (s stFormState) dirty(base Config) bool {
	st := base.SmartThings
	return s.Discovery != st.Discovery ||
		s.ExposeSession != st.ExposeSession ||
		s.effectiveUser() != st.ExposeSessionUser ||
		!slices.Equal(normalizeHubs(s.Hubs), normalizeHubs(st.AllowedHubs))
}

// stRelKey maps the age of the last hub contact to the i18n key of its
// relative-time phrase and the number to format into it; n < 0 means the
// phrase takes no number ("just now").
func stRelKey(age time.Duration) (key string, n int) {
	switch {
	case age < time.Second: // includes a service clock running ahead
		return "st.rel.now", -1
	case age < time.Minute:
		return "st.rel.sec", int(age.Seconds())
	case age < time.Hour:
		return "st.rel.min", int(age.Minutes())
	case age < 24*time.Hour:
		return "st.rel.hour", int(age.Hours())
	default:
		return "st.rel.day", int(age.Hours() / 24)
	}
}

// stRelative renders an RFC3339 timestamp as "3초 전" / "3s ago" relative
// to now. An unparseable or empty value falls back to a neutral phrase.
func (u *ui) stRelative(rfc3339 string, now time.Time) string {
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return u.t("st.rel.unknown")
	}
	key, n := stRelKey(now.Sub(ts))
	if n < 0 {
		return u.t(key)
	}
	return fmt.Sprintf(u.t(key), n)
}

// stHubLine is the connection status line: the hub, the driver version and
// how long ago it last polled, or the "install the driver" hint.
func (u *ui) stHubLine(h STHub, now time.Time) string {
	if !h.Connected || h.IP == "" {
		return u.t("st.hub.none")
	}
	version := h.DriverVersion
	if version == "" {
		version = "?"
	}
	return fmt.Sprintf(u.t("st.hub.connected"), h.IP, version, u.stRelative(h.LastSeen, now))
}

// --- Widgets -----------------------------------------------------------------

// stSection holds the section's widgets; rebuilt with the window on a
// language change, filled by fillSTSection.
type stSection struct {
	status      *widget.Label
	secretHint  *widget.Label
	discovery   *toggle
	session     *toggle
	sessionUser *toggle
	hubBox      *fyne.Container
	addBtn      *widget.Button

	// hubs is the edited allow list and hub the last /api/st/hub result
	// (the [Add current hub] button needs both).
	hubs []string
	hub  STHub

	bar *saveBar
	// filling suppresses the OnChanged cascade while fillSTSection writes
	// the widgets; the dirty state is evaluated once at the end.
	filling bool
}

// state reads the widgets into the pure form model.
func (t *stSection) state() stFormState {
	return stFormState{
		Discovery:         t.discovery.Checked,
		ExposeSession:     t.session.Checked,
		ExposeSessionUser: t.sessionUser.Checked,
		Hubs:              t.hubs,
	}
}

// setUserEnabled greys the "include user name" toggle while session
// exposure is off.
func (t *stSection) setUserEnabled(on bool) {
	if on {
		t.sessionUser.Enable()
	} else {
		t.sessionUser.Disable()
	}
}

// buildSTSection builds the SmartThings part of the network tab. The save
// bar itself is created by buildNetworkTab (it is the whole tab's footer).
func (u *ui) buildSTSection() fyne.CanvasObject {
	t := &stSection{}
	u.st = t
	onToggle := func(bool) { u.updateSTSaveState() }

	t.status = widget.NewLabel(u.t("st.hub.loading"))
	t.status.Wrapping = fyne.TextWrapWord

	// Not an error — the service works without a secret — but worth
	// noticing, so it keeps the warning colour instead of the hint grey.
	t.secretHint = hint(u.t("st.secret.hint"))
	t.secretHint.Importance = widget.WarningImportance
	t.secretHint.Hide()

	t.discovery = newToggle(u.t("st.discovery"), onToggle)
	t.session = newToggle(u.t("st.session"), func(on bool) {
		t.setUserEnabled(on)
		u.updateSTSaveState()
	})
	t.sessionUser = newToggle(u.t("st.session.user"), onToggle)
	t.sessionUser.Disable()

	t.hubBox = container.NewVBox()
	t.addBtn = widget.NewButtonWithIcon(u.t("st.hubs.add"), theme.ContentAddIcon(), func() { u.addCurrentHub() })
	t.addBtn.Disable() // enabled by updateAddHubButton once a hub is known
	u.renderHubs()

	return container.NewVBox(
		t.status,
		t.secretHint,
		t.discovery,
		hint(u.t("st.discovery.hint")),
		t.session,
		hint(u.t("st.session.hint")),
		t.sessionUser,
		hint(u.t("st.session.user.hint")),
		widget.NewSeparator(),
		widget.NewLabelWithStyle(u.t("st.hubs"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		t.hubBox,
		container.NewHBox(t.addBtn, layout.NewSpacer()),
		hint(u.t("st.hubs.hint")),
	)
}

// renderHubs redraws the allow-list rows (IP + [Remove]) from t.hubs. Must
// be called on the UI thread.
func (u *ui) renderHubs() {
	t := u.st
	if t == nil || t.hubBox == nil {
		return
	}
	t.hubBox.RemoveAll()
	if len(t.hubs) == 0 {
		t.hubBox.Add(hint(u.t("st.hubs.empty")))
	}
	for _, ip := range t.hubs {
		// A long list row must not widen the window: the label truncates
		// and the button is pinned to the right edge.
		label := widget.NewLabel(ip)
		label.Truncation = fyne.TextTruncateEllipsis
		del := widget.NewButtonWithIcon(u.t("st.hubs.remove"), theme.DeleteIcon(), func() {
			t.hubs = removeHub(t.hubs, ip)
			u.renderHubs()
			u.updateSTSaveState()
		})
		t.hubBox.Add(container.NewBorder(nil, nil, nil, del, label))
	}
	t.hubBox.Refresh()
	u.updateAddHubButton()
	// The box grew or shrank after the tab was laid out; without this the
	// rows below keep their old positions (see fillSvcBox / #53).
	if u.networkRoot != nil {
		u.networkRoot.Refresh()
	}
}

// addCurrentHub puts the connected hub's IP on the allow list (dirty until
// saved). Must be called on the UI thread.
func (u *ui) addCurrentHub() {
	t := u.st
	if t == nil || !t.hub.Connected {
		return
	}
	t.hubs = addHub(t.hubs, t.hub.IP)
	u.renderHubs()
	u.updateSTSaveState()
}

// updateAddHubButton enables [Add current hub] only while a hub is
// connected and its IP is not on the list yet. UI thread only.
func (u *ui) updateAddHubButton() {
	t := u.st
	if t == nil || t.addBtn == nil {
		return
	}
	if t.hub.Connected && t.hub.IP != "" && !slices.Contains(t.hubs, t.hub.IP) {
		t.addBtn.Enable()
		return
	}
	t.addBtn.Disable()
}

// fillSTSection writes cfg into the section and re-evaluates Save. Must be
// called on the UI thread, after cfgBaseline is set.
func (u *ui) fillSTSection(cfg Config) {
	t := u.st
	if t == nil {
		return
	}
	s := stStateFromConfig(cfg)
	t.filling = true
	t.discovery.SetChecked(s.Discovery)
	t.session.SetChecked(s.ExposeSession)
	t.sessionUser.SetChecked(s.ExposeSessionUser)
	t.setUserEnabled(s.ExposeSession)
	t.hubs = s.Hubs
	u.renderHubs()
	// The secret lives on the settings tab; this only points at it.
	if cfg.Secret == "" {
		t.secretHint.Show()
	} else {
		t.secretHint.Hide()
	}
	t.filling = false
	u.updateSTSaveState()
}

// stDirty reports whether the SmartThings section differs from
// cfgBaseline. False while it is being filled or before a baseline exists.
func (u *ui) stDirty() bool {
	t := u.st
	if t == nil || t.bar == nil || t.filling || u.cfgBaseline == nil {
		return false
	}
	return t.state().dirty(*u.cfgBaseline)
}

// updateSTSaveState enables the network tab's Save, the pulsing indicator
// and the tab marker only while the section differs from cfgBaseline. Safe
// to call before the tab exists. UI thread only.
func (u *ui) updateSTSaveState() {
	t := u.st
	if t == nil || t.bar == nil || t.filling {
		return
	}
	dirty := u.stDirty()
	t.bar.setDirty(dirty)
	u.markTab(tabNetwork, dirty)
}

// saveSTSection posts the baseline with this section's fields written over
// it. quiet skips the "Saved" dialog. Returns false when the save failed.
// UI thread only.
func (u *ui) saveSTSection(quiet bool) bool {
	t := u.st
	if t == nil || u.cfgBaseline == nil {
		return false
	}
	cfg := t.state().applyTo(*u.cfgBaseline)
	msg, err := u.client.SaveConfig(cfg)
	if err != nil {
		dialog.ShowError(err, u.win)
		return false
	}
	// What was just saved is the new "unchanged" state; the other tabs
	// compare against the same baseline.
	u.cfgBaseline = &cfg
	u.fillSTSection(cfg)
	u.updateSaveState()
	u.updateNotifySaveState()
	if !quiet {
		dialog.ShowInformation(u.t("settings.saved"), msg, u.win)
	}
	return true
}

// loadSTHub refreshes the connection status line. Runs off the UI thread.
func (u *ui) loadSTHub() {
	h, err := u.client.GetSTHub()
	if err != nil {
		u.markDisconnectedOnNetError(err)
		return
	}
	now := time.Now()
	fyne.Do(func() {
		t := u.st
		if t == nil {
			return
		}
		t.hub = h
		t.status.SetText(u.stHubLine(h, now))
		u.updateAddHubButton()
	})
}
