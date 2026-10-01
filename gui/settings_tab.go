package gui

// The settings tab: the Windows service, the service settings, media and
// PC notifications (notify_section.go), tools and the app's own options.
// Its pure model is settings_model.go.

import (
	"fmt"
	"os/exec"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/internal/appid"
)

// Grace period choices (seconds) for remote power commands (#51). The
// settings select shows "Off" first, then these.
var graceOptions = []int{10, 30, 60, 300, 600, 1800}

// fallbackGraceSeconds is used when the service reports grace enabled but
// no period (an older service) — matches the service default.
const fallbackGraceSeconds = 300

func (u *ui) buildSettingsTab() fyne.CanvasObject {
	u.portEntry = widget.NewEntry()
	u.secretEntry = widget.NewPasswordEntry()
	// Every edit re-evaluates whether the form differs from the baseline.
	onEdit := func(string) { u.refreshDirty() }
	onToggle := func(bool) { u.refreshDirty() }
	u.portEntry.OnChanged = onEdit
	u.secretEntry.OnChanged = onEdit
	u.remoteCheck = newToggle(u.t("settings.remote"), onToggle)
	u.mediaCheck = newToggle(u.t("settings.media"), onToggle)
	u.graceValues = append([]int{0}, graceOptions...)
	u.graceSelect = widget.NewSelect(u.graceLabels(), func(string) { u.refreshDirty() })

	ft := u.forms.register(&formTab{
		index:   tabSettings,
		Fill:    u.fillSettingsTab,
		Dirty:   func(base Config) bool { return u.settingsState().dirty(base) },
		ApplyTo: func(cfg *Config) error { return u.settingsState().applyTo(cfg, u.lang) },
	})
	ft.bar = newSaveBar(u, func() { u.saveTab(ft) })

	openWebUI := widget.NewButtonWithIcon(u.t("settings.openwebui"), theme.ComputerIcon(), func() {
		_ = exec.Command("cmd", "/c", "start", fmt.Sprintf("http://127.0.0.1:%d", currentWebUIPort())).Start()
	})

	var restartBtn *widget.Button
	restartBtn = widget.NewButtonWithIcon(u.t("settings.restart"), theme.ViewRefreshIcon(), func() {
		dialog.ShowConfirm(u.t("cmd.confirm.title"), u.t("settings.restart.confirm"), func(ok bool) {
			if !ok {
				return
			}
			runAsyncErr(busyControls(restartBtn), u.client.RestartService, func(err error) {
				if err != nil {
					dialog.ShowError(err, u.win)
					return
				}
				u.setStatus(u.t("settings.restarting"))
				u.setConn(connPending)
			})
		}, u.win)
	})

	form := widget.NewForm(
		widget.NewFormItem(u.t("settings.port"), u.portEntry),
		widget.NewFormItem(u.t("settings.secret"), u.secretEntry),
		widget.NewFormItem(u.t("settings.grace"), u.graceSelect),
	)

	settingsBody := container.NewVBox(
		form,
		hint(u.t("settings.grace.hint")),
		u.remoteCheck,
		hint(u.t("settings.remote.hint")),
	)
	// The media switch (media.enabled, #104) heads the 미디어·알림 section,
	// above the PC notification ones (#106, notify_section.go). The
	// now-playing opt-in (#117) is the sharing tab's: it is about what the
	// PC tells others, not what they may do to it.
	mediaNotifyBody := u.buildMediaNotifySection(
		u.mediaCheck, hint(u.t("settings.media.hint")),
	)

	u.svcBox = container.NewVBox()
	u.refreshSvcBox()

	updateCheck := newToggle(u.t("update.check"), func(b bool) {
		u.app.Preferences().SetBool("check_updates", b)
	})
	updateCheck.SetChecked(u.app.Preferences().BoolWithFallback("check_updates", true))
	var manualUpdateBtn *widget.Button
	manualUpdateBtn = widget.NewButtonWithIcon(u.t("update.manual"), theme.DownloadIcon(), func() {
		u.checkForUpdatesManual(manualUpdateBtn)
	})

	autostartCheck := newToggle(u.t("autostart.check"), func(b bool) {
		u.app.Preferences().SetBool("autostart", b)
		if err := SetAutostart(b); err != nil {
			dialog.ShowError(err, u.win)
		}
	})
	autostartCheck.SetChecked(AutostartEnabled())

	// Everything below service management is meaningless until the
	// service is reachable — hidden via applyConnected.
	u.settingsExtra = container.NewVBox(
		widget.NewSeparator(),
		section(u.t("settings.service"), settingsBody),
		widget.NewSeparator(),
		section(u.t("settings.medianotify"), mediaNotifyBody),
		widget.NewSeparator(),
		section(u.t("settings.tools"), container.NewHBox(openWebUI, restartBtn, layout.NewSpacer())),
		widget.NewSeparator(),
		section(u.t("settings.app"), container.NewVBox(
			autostartCheck,
			container.NewHBox(updateCheck, manualUpdateBtn, layout.NewSpacer()),
		)),
		// Trailing padding so the last row never sits flush against the
		// window edge when the tab fits without scrolling.
		widget.NewLabel(""),
	)

	u.settingsRoot = container.NewVBox(
		section(u.t("svc.section"), u.svcBox),
		u.settingsExtra,
	)
	return withSaveBar(u.settingsRoot, ft.bar)
}

// fillSettingsTab writes cfg into the settings-tab fields (the formTab's
// Fill; the coordinator mutes the change callbacks). UI thread only.
func (u *ui) fillSettingsTab(cfg Config) {
	s := settingsStateFromConfig(cfg)
	u.portEntry.SetText(s.Port)
	u.secretEntry.SetText(s.Secret)
	u.remoteCheck.SetChecked(s.Remote)
	u.mediaCheck.SetChecked(s.Media)
	u.setGraceSelection(cfg)
	u.fillNotifySection(cfg)
}

// settingsState reads the settings-tab fields into the pure model.
func (u *ui) settingsState() settingsFormState {
	s := settingsFormState{
		Port:     u.portEntry.Text,
		Secret:   u.secretEntry.Text,
		Remote:   u.remoteCheck.Checked,
		Media:    u.mediaCheck.Checked,
		NotifyPC: u.notifySectionState(),
	}
	if i := u.graceSelect.SelectedIndex(); i > 0 && i < len(u.graceValues) {
		s.GraceOn, s.GraceSec = true, u.graceValues[i]
	}
	return s
}

// graceLabels renders graceValues for the select: "Off" for 0, then the
// periods ("30초", "5분", ...).
func (u *ui) graceLabels() []string {
	labels := make([]string, len(u.graceValues))
	for i, sec := range u.graceValues {
		if sec == 0 {
			labels[i] = u.t("settings.grace.off")
		} else {
			labels[i] = u.formatSeconds(sec)
		}
	}
	return labels
}

// setGraceSelection points the select at cfg. A period outside the presets
// (set through the API) is appended as an extra option so saving other
// settings does not silently change it. Must be called on the UI thread.
func (u *ui) setGraceSelection(cfg Config) {
	if !cfg.ShutdownGrace {
		u.graceSelect.SetSelectedIndex(0)
		return
	}
	sec := cfg.GraceSeconds
	if sec <= 0 {
		sec = fallbackGraceSeconds
	}
	for i, v := range u.graceValues {
		if v == sec {
			u.graceSelect.SetSelectedIndex(i)
			return
		}
	}
	u.graceValues = append(u.graceValues, sec)
	u.graceSelect.Options = u.graceLabels()
	u.graceSelect.SetSelectedIndex(len(u.graceValues) - 1)
}

// refreshSvcBox re-queries the Windows service state and redraws the
// management section.
func (u *ui) refreshSvcBox() {
	background(func() {
		state := queryServiceState()
		fyne.Do(func() { u.fillSvcBox(state) })
	})
}

func (u *ui) fillSvcBox(state svcState) {
	if u.svcBox == nil {
		return
	}

	// Elevated actions block until the spawned process exits (UAC included),
	// then the state and connection refresh immediately — no manual refresh.
	// Every button of the box is busy meanwhile.
	var buttons []fyne.CanvasObject
	elevated := func(run func() error) {
		var ws []fyne.Disableable
		for _, b := range buttons {
			ws = append(ws, b.(fyne.Disableable))
		}
		runAsyncErr(busyControls(ws...), func() error {
			err := run()
			// Give the freshly (un)installed service a moment to settle
			// before re-checking state and connectivity.
			time.Sleep(1500 * time.Millisecond)
			return err
		}, func(err error) {
			if err != nil {
				dialog.ShowError(err, u.win)
			}
			u.refreshSvcBox()
			go u.initialLoad()
		})
	}

	var stateText string

	installBtn := widget.NewButtonWithIcon(u.t("svc.install"), theme.DownloadIcon(), func() {
		install := func() {
			elevated(func() error { return runElevatedSelfWait("install --gui") })
		}
		// config.json / service.log land next to the exe and the service
		// points at this path, so installing from Downloads, Desktop, a
		// temp folder etc. breaks as soon as the file is tidied away. A
		// folder ordinary users can modify gets locked down on install
		// (#126); say so, since files kept there become admin-only.
		exeDir, risk := exeInstallDirRisk()
		if risk == installDirOK {
			install()
			return
		}
		body := widget.NewLabel(fmt.Sprintf(u.t(installDirRiskBodyKey[risk]), exeDir, recommendedInstallDir))
		body.Wrapping = fyne.TextWrapWord
		d := dialog.NewCustomConfirm(u.t("svc.location.title"), u.t("svc.location.anyway"), u.t("login.cancel"),
			body, func(ok bool) {
				if ok {
					install()
				}
			}, u.win)
		d.Resize(fyne.NewSize(460, 0))
		d.Show()
	})
	installBtn.Importance = widget.HighImportance
	startBtn := widget.NewButtonWithIcon(u.t("svc.start"), theme.MediaPlayIcon(), func() {
		elevated(func() error { return runElevatedWait("sc.exe", "start "+serviceName) })
	})
	startBtn.Importance = widget.HighImportance
	uninstallBtn := widget.NewButtonWithIcon(u.t("svc.uninstall"), theme.DeleteIcon(), func() {
		dialog.ShowConfirm(u.t("cmd.confirm.title"), u.t("svc.uninstall.confirm"), func(ok bool) {
			if !ok {
				return
			}
			elevated(func() error {
				if err := runElevatedSelfWait("uninstall"); err != nil {
					return err
				}
				// The Start menu shortcut is this user's (the elevated
				// child may run as another admin), so it goes here, once
				// the service is really gone. Best effort; the next tray
				// start makes it again.
				if queryServiceState() == svcNotInstalled {
					appid.RemoveToastShortcut()
				}
				return nil
			})
		}, u.win)
	})

	switch state {
	case svcRunning:
		stateText = "✓ " + u.t("svc.state.running")
		buttons = []fyne.CanvasObject{uninstallBtn}
	case svcStopped:
		stateText = "■ " + u.t("svc.state.stopped")
		buttons = []fyne.CanvasObject{startBtn, uninstallBtn}
	default:
		stateText = "✗ " + u.t("svc.state.notinstalled")
		buttons = []fyne.CanvasObject{installBtn}
	}

	u.svcBox.RemoveAll()
	u.svcBox.Add(widget.NewLabelWithStyle(stateText, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	u.svcBox.Add(container.NewHBox(append(buttons, layout.NewSpacer())...))
	u.svcBox.Refresh()
	// The box grew after the tab was laid out (the state query is async);
	// without re-laying out the parent, the sections below stay where they
	// were and this one paints over them — visible right after a language
	// change (#53).
	if u.settingsRoot != nil {
		u.settingsRoot.Refresh()
	}
}
