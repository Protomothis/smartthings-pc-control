package gui

import (
	"fmt"
	"slices"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The SmartThings section of the SmartThings tab (issue #70, edge-driver
// doc §7): the Edge driver's connection state, this PC's discovery
// identity and search diagnostics (#95), plus the editable connection part
// of the `smartthings` config (§3.7) — the WoL adapter and the hub allow
// list. What the PC shares (session info, #110 app detection) is the
// sharing tab's (share_tab.go). Only these fields are written over the
// baseline, so a save here never clobbers another tab's edits.
//
// SSDP itself has no switch any more: adding the device has no other path,
// so the responder always runs and the section reports on it instead of
// offering to turn it off.

// stRelative is RelativeTime (viewmodel.go).
func (u *ui) stRelative(rfc3339 string, now time.Time) string {
	return RelativeTime(u.lang, rfc3339, now)
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

// stMachineIDLine is the "이 PC의 ID 58bff996" line. An empty id means the
// service has not answered yet.
func (u *ui) stMachineIDLine(id string) string {
	if id == "" {
		return u.t("st.machineid.unknown")
	}
	return fmt.Sprintf(u.t("st.machineid"), stMachineIDShort(id))
}

// stSearchLine is the discovery diagnostic (export_smartthings.go).
func (u *ui) stSearchLine(s STSSDPState, now time.Time) string {
	return stSearchLine(u.lang, s, now)
}

// stCommandPortLine is the command port line (export_smartthings.go).
func (u *ui) stCommandPortLine(c *STCommandPort, savedPort int) string {
	return stCommandPortLine(u.lang, c, savedPort)
}

// --- Widgets -----------------------------------------------------------------

// stSection holds the section's widgets; rebuilt with the window on a
// language change, filled by fillSTSection.
type stSection struct {
	status     *widget.Label
	secretHint *widget.Label
	machineID  *widget.Label
	copyBtn    *widget.Button
	search     *widget.Label
	cmdPort    *widget.Label // the command port's firewall rule (#139)
	wolSelect  *widget.Select
	wolHint    *widget.Label
	hubBox     *fyne.Container
	addBtn     *widget.Button

	// hubs is the edited allow list and hub the last /api/st/hub result
	// (the [Add current hub] button and the [복사] button need both).
	hubs []string
	hub  STHub

	// wolMAC is the edited smartthings.wol_mac and wolMACs the value each
	// dropdown option saves, in step with wolSelect.Options — the labels
	// are translated prose, so the index is the only reliable link back.
	// wolLoaded stays false until /api/st/hub has answered once, while the
	// dropdown shows its placeholder rather than an empty adapter list.
	wolMAC    string
	wolMACs   []string
	wolLoaded bool

	// filling suppresses the WoL dropdown's OnChanged while fillSTSection
	// writes the widgets.
	filling bool
}

// state reads the widgets into the pure form model.
func (t *stSection) state() stFormState {
	return stFormState{Hubs: t.hubs, WoLMAC: t.wolMAC}
}

// buildSTSection builds the SmartThings part of the SmartThings tab. The
// save bar itself is created by buildSmartThingsTab (the whole tab's
// footer).
func (u *ui) buildSTSection() fyne.CanvasObject {
	t := &stSection{}
	u.st = t

	t.status = widget.NewLabel(u.t("st.hub.loading"))
	t.status.Wrapping = fyne.TextWrapWord

	// Not an error — the service works without a secret — but worth
	// noticing, so it keeps the warning colour instead of the hint grey.
	t.secretHint = hint(u.t("st.secret.hint"))
	t.secretHint.Importance = widget.WarningImportance
	t.secretHint.Hide()

	// This PC's discovery identity and the search diagnostics (#95). Both
	// come from /api/st/hub, which the section already polls.
	t.machineID = widget.NewLabel(u.t("st.machineid.unknown"))
	t.copyBtn = widget.NewButtonWithIcon(u.t("st.machineid.copy"), theme.ContentCopyIcon(), func() { u.copyMachineID() })
	t.copyBtn.Disable() // enabled once the service has told us the id
	t.search = widget.NewLabel(u.t("st.search.loading"))
	t.search.Wrapping = fyne.TextWrapWord
	// Hidden until a service that reports the rule answers.
	t.cmdPort = widget.NewLabel("")
	t.cmdPort.Wrapping = fyne.TextWrapWord
	t.cmdPort.Hide()

	// The adapter dropdown (#96). Its options only exist once /api/st/hub
	// has answered, so it starts as a placeholder; OnChanged is attached
	// after construction so filling it can never look like a user pick.
	t.wolSelect = widget.NewSelect(nil, nil)
	t.wolSelect.PlaceHolder = u.t("st.wol.loading")
	t.wolSelect.OnChanged = func(string) { u.onWoLAdapterPicked() }
	t.wolHint = widget.NewLabel(u.t("st.wol.loading"))
	t.wolHint.Wrapping = fyne.TextWrapWord

	t.hubBox = container.NewVBox()
	t.addBtn = widget.NewButtonWithIcon(u.t("st.hubs.add"), theme.ContentAddIcon(), func() { u.addCurrentHub() })
	t.addBtn.Disable() // enabled by updateAddHubButton once a hub is known
	u.renderHubs()

	return container.NewVBox(
		t.status,
		t.secretHint,
		// The id label keeps its natural width and the button sits next
		// to it rather than stretching across the tab.
		container.NewHBox(t.machineID, t.copyBtn, layout.NewSpacer()),
		t.search,
		t.cmdPort,
		hint(u.t("st.search.hint")),
		widget.NewSeparator(),
		widget.NewLabelWithStyle(u.t("st.wol"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		t.wolSelect,
		t.wolHint,
		hint(u.t("st.wol.hint")),
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
			u.refreshDirty()
		})
		t.hubBox.Add(container.NewBorder(nil, nil, nil, del, label))
	}
	t.hubBox.Refresh()
	u.updateAddHubButton()
	// The box grew or shrank after the tab was laid out; without this the
	// rows below keep their old positions (see fillSvcBox / #53).
	if u.stRoot != nil {
		u.stRoot.Refresh()
	}
}

// onWoLAdapterPicked records the dropdown's choice as the edited wol_mac.
// The labels are translated prose, so the selected index — not the text —
// is what maps back to a MAC. UI thread only.
func (u *ui) onWoLAdapterPicked() {
	t := u.st
	if t == nil || t.filling || t.wolSelect == nil {
		return
	}
	i := t.wolSelect.SelectedIndex()
	if i < 0 || i >= len(t.wolMACs) {
		return
	}
	t.wolMAC = t.wolMACs[i]
	u.renderWoLHint()
	u.refreshDirty()
}

// renderWoLAdapters redraws the dropdown from the last /api/st/hub result,
// keeping the edited selection. Filling the widget must not look like a
// user pick, hence the filling guard. UI thread only.
func (u *ui) renderWoLAdapters() {
	t := u.st
	if t == nil || t.wolSelect == nil || !t.wolLoaded {
		return
	}
	labels, macs := stWoLOptions(u.lang, t.hub.WoL, t.wolMAC)
	was := t.filling
	t.filling = true
	t.wolSelect.Options = labels
	t.wolMACs = macs
	if i := slices.Index(macs, t.wolMAC); i >= 0 {
		t.wolSelect.SetSelectedIndex(i)
	} else {
		// Cannot happen (stWoLOptions keeps an entry for an unknown MAC),
		// but a blank dropdown would be worse than falling back to auto.
		t.wolMAC = ""
		t.wolSelect.SetSelectedIndex(0)
	}
	t.wolSelect.Refresh()
	t.filling = was
	u.renderWoLHint()
}

// renderWoLHint writes the "WoL is off on Ethernet" line for whatever the
// dropdown currently shows. UI thread only.
func (u *ui) renderWoLHint() {
	t := u.st
	if t == nil || t.wolHint == nil || !t.wolLoaded {
		return
	}
	t.wolHint.SetText(stWoLLine(u.lang, stWoLPick(t.hub.WoL, t.wolMAC)))
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
	u.refreshDirty()
}

// copyMachineID puts the full machine id on the clipboard — the label
// only shows the first 8 characters, and the whole value is what a support
// question or a manual device lookup needs. UI thread only.
func (u *ui) copyMachineID() {
	t := u.st
	if t == nil || t.hub.MachineID == "" {
		return
	}
	u.app.Clipboard().SetContent(t.hub.MachineID)
	// Without this the button looks inert: nothing else on screen changes.
	dialog.ShowInformation(u.t("st.machineid.copied"), stMachineIDShort(t.hub.MachineID), u.win)
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

// stForm is the section's formTab.
func (u *ui) stForm(index int) *formTab {
	return &formTab{
		index: index,
		Fill:  u.fillSTSection,
		Dirty: func(base Config) bool { return u.st.state().dirty(base) },
		ApplyTo: func(cfg *Config) error {
			*cfg = u.st.state().applyTo(*cfg)
			return nil
		},
	}
}

// fillSTSection writes cfg into the section (the formTab's Fill). UI
// thread only.
func (u *ui) fillSTSection(cfg Config) {
	t := u.st
	if t == nil {
		return
	}
	s := stStateFromConfig(cfg)
	t.filling = true
	t.hubs = s.Hubs
	t.wolMAC = s.WoLMAC
	u.renderHubs()
	u.renderWoLAdapters()
	t.filling = false
}

// setSTSecretHint shows the "no secret set" warning. It follows the saved
// config (onConfig), not the settings tab's unsaved entry.
func (u *ui) setSTSecretHint(on bool) {
	t := u.st
	if t == nil || t.secretHint == nil {
		return
	}
	if on {
		t.secretHint.Show()
	} else {
		t.secretHint.Hide()
	}
}

// loadSTHub refreshes the connection status line, this PC's id and the
// search diagnostics — all three come from the same poll. Runs off the UI
// thread.
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
		t.wolLoaded = true
		u.renderWoLAdapters()
		t.status.SetText(u.stHubLine(h, now))
		t.machineID.SetText(u.stMachineIDLine(h.MachineID))
		t.search.SetText(u.stSearchLine(h.SSDP, now))
		savedPort := 0
		if u.forms != nil && u.forms.base != nil {
			savedPort = u.forms.base.Port
		}
		if line := u.stCommandPortLine(h.CommandPort, savedPort); line != "" {
			t.cmdPort.SetText(line)
			t.cmdPort.Show()
		} else {
			t.cmdPort.Hide()
		}
		if h.MachineID == "" {
			t.copyBtn.Disable()
		} else {
			t.copyBtn.Enable()
		}
		u.updateAddHubButton()
	})
}
