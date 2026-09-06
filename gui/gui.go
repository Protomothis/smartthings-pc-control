package gui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// cmdButtonSize keeps command buttons compact instead of stretching full-width.
var cmdButtonSize = fyne.NewSize(160, 38)

const webUIPort = 5002

// Commands shown on the test panel. Destructive ones ask for confirmation
// before firing, since a test click acts on this very PC.
var testCommands = []struct {
	Name        string
	LabelKey    string
	Destructive bool
}{
	{"ping", "cmd.ping", false},
	{"lock", "cmd.lock", false},
	{"turnscreenoff", "cmd.screenoff", false},
	{"suspend", "cmd.suspend", true},
	{"hibernate", "cmd.hibernate", true},
	{"restart", "cmd.restart", true},
	{"shutdown", "cmd.shutdown", true},
	{"forceshutdown", "cmd.forceshutdown", true},
}

// Preset delays (minutes) offered as one-click buttons on the schedule tab.
var schedulePresets = []int{5, 15, 30, 60}

// Commands offered for scheduling.
var scheduleCommands = []struct {
	Name     string
	LabelKey string
}{
	{"shutdown", "cmd.shutdown"},
	{"forceshutdown", "cmd.forceshutdown"},
	{"restart", "cmd.restart"},
	{"hibernate", "cmd.hibernate"},
	{"suspend", "cmd.suspend"},
	{"lock", "cmd.lock"},
}

type ui struct {
	app     fyne.App
	win     fyne.Window
	client  *Client
	lang    Lang
	version string

	connected atomic.Bool
	quit      chan struct{}

	// Widgets the background pollers update. Rebuilt on language change.
	status        *widget.Label
	portEntry     *widget.Entry
	secretEntry   *widget.Entry
	logsLabel     *widget.Label
	logsScroll    *container.Scroll
	logsAuto      *widget.Check
	scheduleLabel *widget.Label
	networkBox    *fyne.Container
	svcBox        *fyne.Container
	remoteCheck   *widget.Check
	graceCheck    *widget.Check
	lastSchedCmd  string
	tabs          *container.AppTabs
	// Settings sections hidden until the service is reachable.
	settingsExtra *fyne.Container
	// Tray menu items that only make sense while the service is reachable.
	trayNeedsConn []*fyne.MenuItem
	trayStatus    *fyne.MenuItem
	trayMenu      *fyne.Menu
	// Text mirrored into the tray status entry and icon tooltip: the
	// connection state, the active-schedule countdown ("" when none), and
	// the combined string last pushed to the native menu (dedup).
	statusText string
	schedText  string
	trayShown  string

	// Settings: the config as last loaded/saved. Save is enabled only while
	// the form differs from it; nil until the first successful load.
	saveBtn     *widget.Button
	cfgBaseline *Config

	// Logs: every line from the last fetch; the label shows the subset
	// matching logsFilter. Both touched on the UI thread only.
	logLines   []string
	logsFilter *widget.Entry
}

// Run opens the native GUI window. Blocks until the app quits. With
// minimized set (login autostart) the window stays hidden and only the
// tray icon appears.
func Run(version string, minimized bool) {
	if !acquireSingleInstance() {
		focusExistingWindow()
		return
	}

	a := app.NewWithID("com.protomothis.smartthings-pc-control")
	a.Settings().SetTheme(newKoreanTheme())

	u := &ui{
		app:     a,
		client:  NewClient(webUIPort),
		version: version,
		quit:    make(chan struct{}),
	}

	if saved := a.Preferences().String("lang"); saved != "" {
		u.lang = Lang(saved)
	} else {
		u.lang = systemLang()
	}

	registerToastProtocol()
	// Default on: the grace-period toast only appears while the tray app
	// is running. Rewritten every start so it tracks the current exe path.
	if a.Preferences().BoolWithFallback("autostart", true) {
		SetAutostart(true)
	}

	a.SetIcon(appIcon)
	u.win = a.NewWindow(windowTitle)
	u.win.SetIcon(appIcon)
	u.win.Resize(fyne.NewSize(560, 640))
	// Closing the window hides to the system tray; Exit lives in the tray menu.
	u.win.SetCloseIntercept(func() { u.win.Hide() })

	u.rebuild()
	go u.initialLoad()
	go u.pollLoop()
	if a.Preferences().BoolWithFallback("check_updates", true) {
		// No dialog against a hidden window — tray notification only.
		go u.checkForUpdates(!minimized)
	}

	if minimized {
		// The (unshown) window keeps the Fyne loop alive; the tray menu's
		// Open entry or a left click on the icon shows it later.
		a.Run()
	} else {
		u.win.ShowAndRun()
	}
	close(u.quit)
}

// checkForUpdates queries GitHub Releases; on a newer version it notifies
// the user (dialog on startup, tray notification on periodic checks).
func (u *ui) checkForUpdates(startup bool) {
	rel, err := checkLatestRelease()
	if err != nil || !isNewer(u.version, rel.TagName) {
		return
	}
	// Notify once per discovered version on periodic checks.
	if !startup && u.app.Preferences().String("update_notified") == rel.TagName {
		return
	}
	u.app.Preferences().SetString("update_notified", rel.TagName)

	url := rel.HTMLURL
	if url == "" {
		url = releasesPage
	}
	fyne.Do(func() {
		body := fmt.Sprintf(u.t("update.body"), rel.TagName, u.version)
		u.app.SendNotification(fyne.NewNotification(u.t("update.title"), body))
		if startup {
			dialog.ShowCustomConfirm(u.t("update.title"), u.t("update.open"), u.t("update.later"),
				widget.NewLabel(body), func(ok bool) {
					if ok {
						exec.Command("cmd", "/c", "start", url).Start()
					}
				}, u.win)
		}
	})
}

func (u *ui) t(key string) string { return T(u.lang, key) }

// section renders a subtle bold header above content — lighter than
// widget.Card, which draws a large title and a visible card surface.
func section(title string, content fyne.CanvasObject) fyne.CanvasObject {
	head := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return container.NewVBox(head, content)
}

// rebuild recreates the whole window content in the current language.
func (u *ui) rebuild() {
	// Only the very first build is "connecting"; on a rebuild (language
	// change) show the known state so the bar doesn't flash back to it.
	statusKey := "status.connecting"
	if u.connected.Load() {
		statusKey = "status.connected"
	} else if u.tabs != nil {
		statusKey = "status.unreachable"
	}
	u.statusText = u.t(statusKey)
	u.status = widget.NewLabel(u.statusText)
	// The countdown is re-fetched (in the new language) by initialLoad.
	u.schedText = ""

	langSelect := widget.NewSelect([]string{"한국어", "English"}, func(sel string) {
		newLang := LangEn
		if sel == "한국어" {
			newLang = LangKo
		}
		if newLang == u.lang {
			return
		}
		u.lang = newLang
		u.app.Preferences().SetString("lang", string(newLang))
		// rebuild() replaces every widget with empty ones; keep the
		// current tab and reload config/status/logs/schedule so the
		// window doesn't fall back to blank fields and "Connecting...".
		selected := u.tabs.SelectedIndex()
		u.rebuild()
		if u.connected.Load() && selected >= 0 && selected < len(u.tabs.Items) {
			u.tabs.SelectIndex(selected)
		}
		go u.initialLoad()
	})
	if u.lang == LangKo {
		langSelect.SetSelected("한국어")
	} else {
		langSelect.SetSelected("English")
	}

	versionLabel := widget.NewLabel(u.version)
	versionLabel.Importance = widget.LowImportance
	topBar := container.NewBorder(nil, nil, u.status, container.NewHBox(versionLabel, langSelect))

	u.tabs = container.NewAppTabs(
		container.NewTabItem(u.t("tab.settings"), u.buildSettingsTab()),
		container.NewTabItem(u.t("tab.commands"), u.buildCommandsTab()),
		container.NewTabItem(u.t("tab.schedule"), u.buildScheduleTab()),
		container.NewTabItem(u.t("tab.network"), u.buildNetworkTab()),
		container.NewTabItem(u.t("tab.logs"), u.buildLogsTab()),
	)

	u.win.SetContent(container.NewBorder(topBar, nil, nil, nil, u.tabs))
	u.setupTray()
	u.refreshTrayStatus()
	u.applyConnected(u.connected.Load())
}

// applyConnected gates UI that needs the service: while unreachable, only
// the Settings tab (install/start lives there) and basic tray entries stay
// usable. Must be called on the UI thread.
func (u *ui) applyConnected(on bool) {
	for i := 1; i < len(u.tabs.Items); i++ {
		if on {
			u.tabs.EnableIndex(i)
		} else {
			u.tabs.DisableIndex(i)
		}
	}
	if !on {
		u.tabs.SelectIndex(0)
	}
	if u.settingsExtra != nil {
		if on {
			u.settingsExtra.Show()
		} else {
			u.settingsExtra.Hide()
		}
	}
	for _, item := range u.trayNeedsConn {
		item.Disabled = !on
	}
	if u.trayMenu != nil {
		u.trayMenu.Refresh()
	}
}

// --- Tabs ---

func (u *ui) buildSettingsTab() fyne.CanvasObject {
	u.portEntry = widget.NewEntry()
	u.secretEntry = widget.NewPasswordEntry()
	// Every edit re-evaluates whether the form differs from the baseline.
	onEdit := func(string) { u.updateSaveState() }
	onToggle := func(bool) { u.updateSaveState() }
	u.portEntry.OnChanged = onEdit
	u.secretEntry.OnChanged = onEdit
	u.remoteCheck = widget.NewCheck(u.t("settings.remote"), onToggle)
	u.graceCheck = widget.NewCheck(u.t("settings.grace"), onToggle)

	u.saveBtn = widget.NewButtonWithIcon(u.t("settings.save"), theme.DocumentSaveIcon(), func() {
		port, err := strconv.Atoi(strings.TrimSpace(u.portEntry.Text))
		if err != nil {
			dialog.ShowError(errors.New(u.t("settings.invalidport")+u.portEntry.Text), u.win)
			return
		}
		if u.remoteCheck.Checked && u.secretEntry.Text == "" {
			dialog.ShowError(errors.New(u.t("settings.remote.needsecret")), u.win)
			return
		}
		cfg := Config{
			Port:          port,
			Secret:        u.secretEntry.Text,
			WebUIRemote:   u.remoteCheck.Checked,
			ShutdownGrace: u.graceCheck.Checked,
		}
		msg, err := u.client.SaveConfig(cfg)
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		// What was just saved is the new "unchanged" state.
		u.cfgBaseline = &cfg
		u.updateSaveState()
		dialog.ShowInformation(u.t("settings.saved"), msg, u.win)
	})
	u.saveBtn.Importance = widget.HighImportance
	// Nothing to compare against until initialLoad fills the form (the
	// fields are empty on a language-change rebuild too).
	u.cfgBaseline = nil
	u.saveBtn.Disable()

	openWebUI := widget.NewButtonWithIcon(u.t("settings.openwebui"), theme.ComputerIcon(), func() {
		exec.Command("cmd", "/c", "start", fmt.Sprintf("http://127.0.0.1:%d", webUIPort)).Start()
	})

	restartBtn := widget.NewButtonWithIcon(u.t("settings.restart"), theme.ViewRefreshIcon(), func() {
		dialog.ShowConfirm(u.t("cmd.confirm.title"), u.t("settings.restart.confirm"), func(ok bool) {
			if !ok {
				return
			}
			if err := u.client.RestartService(); err != nil {
				dialog.ShowError(err, u.win)
				return
			}
			u.setStatus(u.t("settings.restarting"))
		}, u.win)
	})

	form := widget.NewForm(
		widget.NewFormItem(u.t("settings.port"), u.portEntry),
		widget.NewFormItem(u.t("settings.secret"), u.secretEntry),
	)

	settingsBody := container.NewVBox(
		form,
		u.remoteCheck,
		u.graceCheck,
		container.NewHBox(layout.NewSpacer(), u.saveBtn),
	)

	u.svcBox = container.NewVBox()
	u.refreshSvcBox()

	updateCheck := widget.NewCheck(u.t("update.check"), func(b bool) {
		u.app.Preferences().SetBool("check_updates", b)
	})
	updateCheck.SetChecked(u.app.Preferences().BoolWithFallback("check_updates", true))

	autostartCheck := widget.NewCheck(u.t("autostart.check"), func(b bool) {
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
		section(u.t("settings.tools"), container.NewVBox(
			container.NewHBox(openWebUI, restartBtn, layout.NewSpacer()),
			autostartCheck,
			updateCheck,
		)),
	)

	return container.NewVScroll(container.NewPadded(container.NewVBox(
		section(u.t("svc.section"), u.svcBox),
		u.settingsExtra,
	)))
}

// settingsDirty reports whether the form differs from the loaded/saved
// config. False (nothing to save) until a baseline exists.
func (u *ui) settingsDirty() bool {
	b := u.cfgBaseline
	if b == nil {
		return false
	}
	return strings.TrimSpace(u.portEntry.Text) != strconv.Itoa(b.Port) ||
		u.secretEntry.Text != b.Secret ||
		u.remoteCheck.Checked != b.WebUIRemote ||
		u.graceCheck.Checked != b.ShutdownGrace
}

// updateSaveState enables Save only while there is something to save.
// Must be called on the UI thread.
func (u *ui) updateSaveState() {
	if u.saveBtn == nil {
		return
	}
	if u.settingsDirty() {
		u.saveBtn.Enable()
	} else {
		u.saveBtn.Disable()
	}
}

// refreshSvcBox re-queries the Windows service state and redraws the
// management section.
func (u *ui) refreshSvcBox() {
	go func() {
		state := queryServiceState()
		fyne.Do(func() { u.fillSvcBox(state) })
	}()
}

func (u *ui) fillSvcBox(state svcState) {
	if u.svcBox == nil {
		return
	}

	// Elevated actions block until the spawned process exits (UAC included),
	// then the state and connection refresh immediately — no manual refresh.
	elevated := func(run func() error) {
		go func() {
			if err := run(); err != nil {
				fyne.Do(func() { dialog.ShowError(err, u.win) })
			}
			// Give the freshly (un)installed service a moment to settle
			// before re-checking state and connectivity.
			time.Sleep(1500 * time.Millisecond)
			u.refreshSvcBox()
			go u.initialLoad()
		}()
	}

	var stateText string
	var buttons []fyne.CanvasObject

	installBtn := widget.NewButtonWithIcon(u.t("svc.install"), theme.DownloadIcon(), func() {
		install := func() {
			elevated(func() error { return runElevatedSelfWait("install --gui") })
		}
		// config.json / service.log land next to the exe and the service
		// points at this path, so installing from Downloads, Desktop, a
		// temp folder etc. breaks as soon as the file is tidied away.
		exeDir, risky := exeInRiskyDir()
		if !risky {
			install()
			return
		}
		body := widget.NewLabel(fmt.Sprintf(u.t("svc.location.body"), exeDir, recommendedInstallDir))
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
			elevated(func() error { return runElevatedSelfWait("uninstall") })
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
}

func (u *ui) buildCommandsTab() fyne.CanvasObject {
	makeButton := func(name, label string, destructive bool) fyne.CanvasObject {
		run := func() {
			if _, err := u.client.TestCommand(name); err != nil {
				dialog.ShowError(err, u.win)
				return
			}
			u.setStatus(fmt.Sprintf(u.t("cmd.sent"), label))
		}
		btn := widget.NewButton(label, func() {
			if destructive {
				dialog.ShowConfirm(u.t("cmd.confirm.title"), fmt.Sprintf(u.t("cmd.confirm.body"), label), func(ok bool) {
					if ok {
						run()
					}
				}, u.win)
				return
			}
			run()
		})
		if destructive {
			btn.Importance = widget.DangerImportance
		}
		return btn
	}

	var safe, power []fyne.CanvasObject
	for _, tc := range testCommands {
		btn := makeButton(tc.Name, u.t(tc.LabelKey), tc.Destructive)
		if tc.Destructive {
			power = append(power, btn)
		} else {
			safe = append(safe, btn)
		}
	}

	note := widget.NewLabel(u.t("cmd.immediate.note"))
	note.Wrapping = fyne.TextWrapWord
	note.Importance = widget.LowImportance

	return container.NewVScroll(container.NewPadded(container.NewVBox(
		section(u.t("cmd.group.safe"), container.NewGridWrap(cmdButtonSize, safe...)),
		widget.NewSeparator(),
		section(u.t("cmd.group.power"), container.NewGridWrap(cmdButtonSize, power...)),
		note,
	)))
}

func (u *ui) buildScheduleTab() fyne.CanvasObject {
	u.scheduleLabel = widget.NewLabel(u.t("schedule.none"))

	labels := make([]string, len(scheduleCommands))
	for i, sc := range scheduleCommands {
		labels[i] = u.t(sc.LabelKey)
	}
	cmdSelect := widget.NewSelect(labels, nil)
	cmdSelect.SetSelectedIndex(0)

	minutesEntry := widget.NewEntry()
	minutesEntry.SetText("30")

	// One-click presets that just fill the minutes entry.
	presets := []fyne.CanvasObject{}
	for _, m := range schedulePresets {
		m := m
		b := widget.NewButton(fmt.Sprintf(u.t("schedule.preset"), m), func() {
			minutesEntry.SetText(strconv.Itoa(m))
		})
		b.Importance = widget.LowImportance
		presets = append(presets, b)
	}
	presets = append(presets, layout.NewSpacer())
	presetRow := container.NewHBox(presets...)

	startBtn := widget.NewButtonWithIcon(u.t("schedule.start"), theme.MediaPlayIcon(), func() {
		minutes, err := strconv.Atoi(strings.TrimSpace(minutesEntry.Text))
		if err != nil || minutes < 1 || minutes > 1440 {
			dialog.ShowError(errors.New(u.t("schedule.invalid")), u.win)
			return
		}
		name := scheduleCommands[cmdSelect.SelectedIndex()].Name
		label := u.t(scheduleCommands[cmdSelect.SelectedIndex()].LabelKey)
		dialog.ShowConfirm(u.t("cmd.confirm.title"), fmt.Sprintf(u.t("cmd.confirm.body"), label), func(ok bool) {
			if !ok {
				return
			}
			if err := u.client.SetSchedule(name, minutes); err != nil {
				dialog.ShowError(err, u.win)
				return
			}
			u.refreshNow()
		}, u.win)
	})
	startBtn.Importance = widget.HighImportance

	cancelBtn := widget.NewButtonWithIcon(u.t("schedule.cancel"), theme.CancelIcon(), func() {
		if err := u.client.CancelSchedule(); err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		u.refreshNow()
	})

	form := widget.NewForm(
		widget.NewFormItem(u.t("schedule.command"), cmdSelect),
		widget.NewFormItem(u.t("schedule.minutes"), container.NewVBox(minutesEntry, presetRow)),
	)

	u.scheduleLabel.TextStyle = fyne.TextStyle{Bold: true}
	return container.NewVScroll(container.NewPadded(container.NewVBox(
		u.scheduleLabel,
		widget.NewSeparator(),
		form,
		container.NewHBox(layout.NewSpacer(), startBtn, cancelBtn),
	)))
}

func (u *ui) buildNetworkTab() fyne.CanvasObject {
	u.networkBox = container.NewVBox(widget.NewLabel(u.t("network.loading")))
	refreshBtn := widget.NewButtonWithIcon(u.t("network.refresh"), theme.ViewRefreshIcon(), func() { go u.loadNetwork() })
	go u.loadNetwork()
	top := container.NewHBox(layout.NewSpacer(), refreshBtn)
	return container.NewBorder(top, nil, nil, nil, container.NewVScroll(u.networkBox))
}

func (u *ui) buildLogsTab() fyne.CanvasObject {
	u.logsLabel = widget.NewLabel(u.t("logs.empty"))
	u.logsLabel.Wrapping = fyne.TextWrapBreak
	u.logsLabel.TextStyle = fyne.TextStyle{Monospace: true}
	u.logsScroll = container.NewVScroll(u.logsLabel)

	u.logsAuto = widget.NewCheck(u.t("logs.autorefresh"), nil)
	u.logsAuto.SetChecked(true)
	refreshBtn := widget.NewButtonWithIcon(u.t("logs.refresh"), theme.ViewRefreshIcon(), func() { go u.loadLogs() })

	// Case-insensitive substring filter over the cached lines; re-rendered
	// on every keystroke, and applied by loadLogs on each refresh.
	u.logsFilter = widget.NewEntry()
	u.logsFilter.SetPlaceHolder(u.t("logs.filter"))
	u.logsFilter.OnChanged = func(string) { u.renderLogs() }
	u.logLines = nil

	openFileBtn := widget.NewButtonWithIcon(u.t("logs.openfile"), theme.DocumentIcon(), func() {
		u.openServiceLog(false)
	})
	openDirBtn := widget.NewButtonWithIcon(u.t("logs.openfolder"), theme.FolderOpenIcon(), func() {
		u.openServiceLog(true)
	})

	top := container.NewBorder(nil, nil, u.logsAuto, container.NewHBox(openFileBtn, openDirBtn, refreshBtn), u.logsFilter)
	return container.NewBorder(top, nil, nil, nil, u.logsScroll)
}

// serviceLogPath is service.log next to the exe — the same location the
// service (and localSecret) use.
func serviceLogPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "service.log"), nil
}

// openServiceLog opens service.log in the default viewer, or reveals it in
// Explorer when folder is set. Shows an error if the file does not exist.
func (u *ui) openServiceLog(folder bool) {
	path, err := serviceLogPath()
	if err == nil {
		_, err = os.Stat(path)
	}
	if err != nil {
		dialog.ShowError(fmt.Errorf(u.t("logs.notfound"), path), u.win)
		return
	}
	var cmd *exec.Cmd
	if folder {
		// explorer.exe is a GUI app (no console to hide); build the command
		// line by hand so the "/select," switch and path stay one argument.
		cmd = exec.Command("explorer.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: fmt.Sprintf(`explorer.exe /select,"%s"`, path)}
	} else {
		// `start "" <file>` opens with the file's associated app; hide the
		// helper console since the GUI is built with -H=windowsgui.
		cmd = exec.Command("cmd", "/c", "start", "", path)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
	if err := cmd.Start(); err != nil {
		dialog.ShowError(err, u.win)
	}
}

// --- Data loading / polling ---

func (u *ui) initialLoad() {
	cfg, err := u.client.GetConfig()
	fyne.Do(func() {
		if errors.Is(err, errUnauthorized) {
			u.promptLogin(func() { go u.initialLoad() })
			return
		}
		if err != nil {
			u.connected.Store(false)
			u.setStatus(u.t("status.unreachable"))
			u.applyConnected(false)
			return
		}
		u.connected.Store(true)
		u.setStatus(u.t("status.connected"))
		u.applyConnected(true)
		// Baseline first: SetText/SetChecked fire OnChanged, which compares
		// against it; once all four match, Save ends up disabled.
		u.cfgBaseline = &cfg
		u.portEntry.SetText(strconv.Itoa(cfg.Port))
		u.secretEntry.SetText(cfg.Secret)
		u.remoteCheck.SetChecked(cfg.WebUIRemote)
		u.graceCheck.SetChecked(cfg.ShutdownGrace)
		u.updateSaveState()
	})
	if err == nil {
		u.loadLogs()
		u.loadSchedule()
	}
}

// pollLoop drives periodic refreshes until the window closes.
func (u *ui) pollLoop() {
	logsTick := time.NewTicker(3 * time.Second)
	schedTick := time.NewTicker(2 * time.Second)
	connTick := time.NewTicker(5 * time.Second)
	updateTick := time.NewTicker(24 * time.Hour)
	defer logsTick.Stop()
	defer schedTick.Stop()
	defer connTick.Stop()
	defer updateTick.Stop()

	for {
		select {
		case <-u.quit:
			return
		case <-updateTick.C:
			if u.app.Preferences().BoolWithFallback("check_updates", true) {
				go u.checkForUpdates(false)
			}
		case <-logsTick.C:
			if u.connected.Load() && u.logsAuto != nil && u.logsAuto.Checked {
				u.loadLogs()
			}
		case <-schedTick.C:
			if u.connected.Load() {
				u.loadSchedule()
			}
		case <-connTick.C:
			if !u.connected.Load() {
				go u.initialLoad()
			}
		}
	}
}

func (u *ui) refreshNow() {
	go u.loadSchedule()
	go u.loadLogs()
}

func (u *ui) loadLogs() {
	lines, err := u.client.Logs()
	if err != nil {
		u.markDisconnectedOnNetError(err)
		return
	}
	fyne.Do(func() {
		u.logLines = lines
		u.renderLogs()
	})
}

// renderLogs shows the cached lines that match the filter, keeping the
// view pinned to the end when it already was. Must be called on the UI
// thread.
func (u *ui) renderLogs() {
	var filter string
	if u.logsFilter != nil {
		filter = strings.ToLower(strings.TrimSpace(u.logsFilter.Text))
	}
	var shown []string
	for _, line := range u.logLines {
		if filter == "" || strings.Contains(strings.ToLower(line), filter) {
			shown = append(shown, line)
		}
	}
	if len(shown) == 0 {
		if filter != "" && len(u.logLines) > 0 {
			u.logsLabel.SetText(u.t("logs.nomatch"))
		} else {
			u.logsLabel.SetText(u.t("logs.empty"))
		}
		return
	}
	atBottom := u.logsScroll.Offset.Y >= u.logsScroll.Content.Size().Height-u.logsScroll.Size().Height-20
	u.logsLabel.SetText(strings.Join(shown, "\n"))
	if atBottom {
		u.logsScroll.ScrollToBottom()
	}
}

func (u *ui) loadSchedule() {
	s, err := u.client.GetSchedule()
	if err != nil {
		u.markDisconnectedOnNetError(err)
		return
	}
	fyne.Do(func() {
		if !s.Active {
			u.lastSchedCmd = ""
			u.scheduleLabel.SetText(u.t("schedule.none"))
			u.setScheduleText("")
			return
		}
		d := time.Duration(s.RemainingSec) * time.Second
		hh := int(d.Hours())
		mm := int(d.Minutes()) % 60
		ss := int(d.Seconds()) % 60
		var remain string
		if hh > 0 {
			remain = fmt.Sprintf("%d:%02d:%02d", hh, mm, ss)
		} else {
			remain = fmt.Sprintf("%02d:%02d", mm, ss)
		}
		cmdLabel := s.Command
		for _, sc := range scheduleCommands {
			if sc.Name == s.Command {
				cmdLabel = u.t(sc.LabelKey)
			}
		}
		countdown := fmt.Sprintf(u.t("schedule.countdown"), cmdLabel, remain)
		u.scheduleLabel.SetText(countdown)
		// Same line in the tray entry and icon tooltip.
		u.setScheduleText(countdown)

		// A schedule appeared (SmartThings grace period, WebUI, or this
		// app) — notify with Run now / Cancel buttons so it can be
		// handled straight from the toast.
		if s.Command != u.lastSchedCmd {
			u.lastSchedCmd = s.Command
			lang := u.lang
			go func() {
				if err := showGraceToast(lang, cmdLabel, remain); err != nil {
					// Toast failed (e.g. PowerShell unavailable) — plain notification.
					u.app.SendNotification(fyne.NewNotification(T(lang, "notify.grace.title"),
						fmt.Sprintf(T(lang, "notify.grace.body"), cmdLabel, remain)))
				}
			}()
		}
	})
}

func (u *ui) loadNetwork() {
	s, err := u.client.GetWoLStatus()
	if err != nil {
		u.markDisconnectedOnNetError(err)
		return
	}
	fyne.Do(func() {
		u.networkBox.RemoveAll()
		if s.Error != "" {
			u.networkBox.Add(widget.NewLabel(s.Error))
			return
		}
		if s.Ready {
			u.networkBox.Add(widget.NewLabelWithStyle("✓ "+u.t("network.wolready"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		} else {
			u.networkBox.Add(widget.NewLabelWithStyle("✗ "+u.t("network.wolnotready"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		}
		if s.Warning != "" {
			u.networkBox.Add(widget.NewLabel(s.Warning))
		}
		if s.ExternalIP != "" {
			u.networkBox.Add(widget.NewLabel(u.t("network.externalip") + ": " + s.ExternalIP))
		}
		u.networkBox.Add(widget.NewSeparator())
		for _, ad := range s.Adapters {
			state := u.t("network.adapter.down")
			if ad.Status == "Up" {
				state = u.t("network.adapter.up")
			}
			wol := u.t("network.wol.off")
			if ad.WoLEnabled {
				wol = u.t("network.wol.on")
			}
			title := fmt.Sprintf("%s — %s / %s", ad.Name, state, wol)
			detail := "MAC: " + ad.MacAddress
			if len(ad.IPs) > 0 {
				detail += "\nIP: " + strings.Join(ad.IPs, ", ")
			}
			u.networkBox.Add(section(title, widget.NewLabel(detail)))
			u.networkBox.Add(widget.NewSeparator())
		}
	})
}

// markDisconnectedOnNetError flips the status bar when the service goes away.
func (u *ui) markDisconnectedOnNetError(err error) {
	if errors.Is(err, errUnauthorized) {
		return
	}
	if u.connected.Swap(false) {
		fyne.Do(func() {
			u.setStatus(u.t("status.unreachable"))
			u.applyConnected(false)
		})
	}
}

// promptLogin shows a password dialog and calls onSuccess after a valid login.
func (u *ui) promptLogin(onSuccess func()) {
	secretEntry := widget.NewPasswordEntry()
	items := []*widget.FormItem{widget.NewFormItem(u.t("login.secret"), secretEntry)}
	dialog.ShowForm(u.t("login.title"), u.t("login.ok"), u.t("login.cancel"), items, func(ok bool) {
		if !ok {
			return
		}
		secret := secretEntry.Text
		go func() {
			err := u.client.Login(secret)
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, u.win)
					u.promptLogin(onSuccess)
					return
				}
				onSuccess()
			})
		}()
	}, u.win)
}
