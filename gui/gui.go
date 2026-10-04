package gui

// The desktop app: the window, its status bar and tabs, and the loops that
// keep them current. Each tab lives in a file of its own (settings_tab.go,
// commands_tab.go, schedule_tab.go, notify_tab.go, network_tab.go,
// presets_tab.go, logs_tab.go); the forms they share are coordinated in
// forms.go, and every click that calls the service goes through runAsync
// (async.go) against serviceAPI (service_api.go).

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// defaultWebUIPort is the API port when tray.json is absent (SmartThings
// port 5001 + 1). The live value comes from localWebUIPort().
const defaultWebUIPort = 5002

// webUIPort is the port u.client talks to: resolved at start from
// tray.json next to the exe, and moved by setPort when the service comes
// back on a new port (#121). Read it with currentWebUIPort.
var webUIPort atomic.Int32

// currentWebUIPort is webUIPort, or the default before Run has set it.
func currentWebUIPort() int {
	if p := webUIPort.Load(); p != 0 {
		return int(p)
	}
	return defaultWebUIPort
}

type ui struct {
	app     fyne.App
	win     fyne.Window
	client  serviceAPI
	lang    Lang
	version string

	connected atomic.Bool
	// login is a loginState (login.go): keeps a single login dialog up
	// and stops connTick from re-prompting over it (#98).
	login atomic.Int32
	// localGate holds back the automatic local login after a refusal
	// (locallogin.go, #131).
	localGate localLoginGate
	// trustAt is a trustState (locallogin.go): whether the service vouched
	// for this process, which the presets and watch editors need (C5).
	trustAt atomic.Int32
	// confirm asks a yes/no question; nil is Fyne's confirm dialog. The
	// tests answer it themselves.
	confirm func(title, body, ok string, cb func(bool))
	quit    chan struct{}

	// visible is whether the window was on screen at pollLoop's last look,
	// shownTab the tab in front (mirrors curTab) — both read by pollLoop.
	// deferContent is set while a minimized start has not shown the window
	// yet: until then only the tray exists (visibility.go).
	visible      atomic.Bool
	shownTab     atomic.Int32
	deferContent bool

	// Widgets the background pollers update. Rebuilt on language change.
	status      *widget.Label
	loginBtn    *widget.Button // shown only while the login is deferred
	statusDot   *canvas.Circle // connection indicator next to the status text
	portEntry   *widget.Entry
	secretEntry *widget.Entry
	logsScroll  *container.Scroll
	logsAuto    *widget.Check
	// logsAutoOn mirrors logsAuto.Checked for pollLoop, which must not read
	// the widget off the UI goroutine.
	logsAutoOn atomic.Bool
	// Schedule tab: big remaining time, the command line under it, and the
	// cancel button that is only enabled while a schedule is active.
	schedBig       *widget.RichText
	scheduleLabel  *widget.Label
	schedCancelBtn *widget.Button
	// Command tab: the keep-awake row (#111, awake.go).
	awake *awakeRow
	// Command tab: the media card (#117, media_card.go). lastMedia (and
	// whether it was ever loaded) survives a rebuild so the card comes
	// back at once; hbSent is what the tray's 3s check last delivered.
	media        *mediaCard
	lastMedia    MediaState
	lastMediaErr error
	mediaLoaded  bool
	hbSent       heartbeatSent
	// Command tab: the [실행] buttons of the saved presets (#109) and the
	// line shown when there are none; the presets tab (presets_tab.go) and
	// the settings tab's PC-notification switches (notify_section.go, #106).
	presetButtons *fyne.Container
	presetEmpty   *widget.Label
	presets       *presetsTab
	pcNotify      *notifySection
	// Status bar battery label (#112, battery.go), hidden without a
	// battery; lastBattery survives a rebuild so the label comes back at once.
	batteryLabel *widget.Label
	lastBattery  Battery
	// SmartThings tab: the WoL/adapter list, the tab root (re-laid out when
	// the hub list changes) and the SmartThings section (#70); the sharing
	// tab (share_tab.go).
	networkBox *fyne.Container
	stRoot     *fyne.Container
	st         *stSection
	share      *shareTab
	// Settings tab: the service box, browser access and media.enabled
	// (#104: volume and media-key commands).
	svcBox      *fyne.Container
	remoteCheck *toggle
	mediaCheck  *toggle
	// debugCheck is the developer section's debug mode switch (#133).
	debugCheck *toggle
	// Grace select: graceValues[i] is the period (seconds) behind option i;
	// 0 is the leading "Off" entry. A period not in graceOptions (set via
	// the API) is appended so it round-trips unchanged.
	graceSelect  *widget.Select
	graceValues  []int
	lastSchedCmd string
	tabs         *container.AppTabs
	// Settings tab root (re-laid out when the async service state arrives)
	// and the sections hidden until the service is reachable.
	settingsRoot  *fyne.Container
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

	// forms is the save coordinator (forms.go): the config as last loaded
	// or saved, and the tabs that edit it. It survives a rebuild.
	forms  *forms
	notify *notifyTab
	// Tab titles without the "•" unsaved marker, the tab shown before the
	// current selection, and a guard for programmatic SelectIndex calls
	// (see savebar.go). returnTab is the tab (+1; 0 = none) to go back to
	// when the service answers again after a forced switch to settings.
	tabTitles []string
	curTab    int
	switching bool
	returnTab int

	// Logs: every line from the last fetch; the view shows the subset
	// matching logsFilter. logsShown is what the view shows (nil while it
	// shows the logsShownMsg locale key instead), so an unchanged poll
	// leaves it alone. logsOffsetY is the view's offset as last seen, to
	// tell a scroll up from a scroll down; logsScrolling marks our own
	// scrolls (logs_tab.go). logsRows holds a row per shown line, from
	// logRowPool (logs_rows.go); logsMsg stands in for it with a message.
	// All touched on the UI thread only.
	logsRows       *fyne.Container
	logsRowsLayout *logRowsLayout
	logRowPool     []*logRow
	logsMsg        *widget.RichText
	logLines       []string
	logsFilter     *widget.Entry
	logsShown      []string
	logsShownMsg   string
	logsOffsetY    float32
	logsScrolling  bool
}

// Run opens the native GUI window. Blocks until the app quits. With
// minimized set (login autostart) the window stays hidden and only the
// tray icon appears.
func Run(version string, minimized bool) {
	inst := claimInstance(instanceMutexName, activateEventName)
	if !inst.first {
		// A minimized launch (login autostart, or the service waking the
		// tray app for a grace toast) must stay silent: the running
		// instance already has the tray and will show the toast itself.
		// Any other launch (Start menu, double-click, the updater's
		// relaunch) opens the running instance's window, which a minimized
		// start may not have created yet (singleinstance.go).
		if !minimized {
			activateRunning(inst)
		}
		return
	}
	// Debug mode (#133, debug.go): tray.json's switch, before anything
	// else can crash; the adopted config takes over once the service
	// answers. A normal quit removes the file again.
	startDebugMode(version, readLocalConfig().Debug)
	defer stopDebugMode()

	a := app.NewWithID("com.protomothis.smartthings-pc-control")
	a.Settings().SetTheme(newKoreanTheme())

	webUIPort.Store(int32(localWebUIPort()))
	u := &ui{
		app:     a,
		client:  NewClient(currentWebUIPort()),
		version: version,
		quit:    make(chan struct{}),
	}

	if saved := a.Preferences().String("lang"); saved != "" {
		u.lang = Lang(saved)
	} else {
		u.lang = systemLang()
	}

	registerToastProtocol()
	// Toasts show as banners only under the AUMID of a Start menu
	// shortcut; made or repaired (exe moved) on every start.
	go ensureToastShortcut()
	// Default on: the grace-period toast only appears while the tray app
	// is running. Rewritten every start so it tracks the current exe path.
	if a.Preferences().BoolWithFallback("autostart", true) {
		SetAutostart(true)
	}
	go cleanupStaleUpdateFiles() // leftovers from a previous self-update

	a.SetIcon(appIcon)
	u.win = a.NewWindow(windowTitle)
	u.win.SetIcon(appIcon)
	// Tall enough for the Settings tab (the longest one) to show without a
	// scrollbar in either language; see #53.
	u.win.Resize(fyne.NewSize(640, 800))
	// Closing the window hides to the system tray (after an unsaved-changes
	// prompt when needed, installed by rebuild); Exit lives in the tray menu.
	//
	// A minimized start keeps the content out of the window until it is
	// first shown, so nothing measures text (and loads the fonts) before
	// someone looks. Gaining focus is the catch-all for every way of
	// showing it; the tray entries and pollLoop call ensureContent too.
	u.deferContent = minimized
	a.Lifecycle().SetOnEnteredForeground(u.ensureContent)
	u.rebuild()
	go u.initialLoad()
	go u.pollLoop()
	if a.Preferences().BoolWithFallback("check_updates", true) {
		// No dialog against a hidden window — tray notification only.
		go u.checkForUpdates(!minimized)
	}
	// A later launch of the app opens the window like the tray's Open; one
	// that came while this was starting is waiting in the event already.
	stopActivation := watchActivation(inst.activate, func() { fyne.Do(u.showWindow) })
	defer stopActivation()

	if minimized {
		// The (unshown) window keeps the Fyne loop alive; the tray menu's
		// Open entry or a left click on the icon shows it later.
		a.Run()
	} else {
		u.win.ShowAndRun()
	}
	close(u.quit)
}

func (u *ui) t(key string) string { return T(u.lang, key) }

// formatSeconds renders whole minutes as "5분"/"5 min" and anything
// shorter (or uneven) as seconds.
func (u *ui) formatSeconds(sec int) string {
	if sec >= 60 && sec%60 == 0 {
		return fmt.Sprintf(u.t("duration.min"), sec/60)
	}
	return fmt.Sprintf(u.t("duration.sec"), sec)
}

// formatMinutes renders a schedule delay in the largest unit that fits:
// "45분", "1시간 30분", "3일" (#89). The presets now reach three days, and
// "4320분" is not a label anyone reads as that.
func (u *ui) formatMinutes(min int) string {
	switch {
	case min < 60:
		return fmt.Sprintf(u.t("duration.min"), min)
	case min < 1440:
		if rest := min % 60; rest != 0 {
			return fmt.Sprintf(u.t("duration.hourmin"), min/60, rest)
		}
		return fmt.Sprintf(u.t("duration.hour"), min/60)
	default:
		// The odd minutes are noise at a day's distance.
		if hours := (min % 1440) / 60; hours != 0 {
			return fmt.Sprintf(u.t("duration.dayhour"), min/1440, hours)
		}
		return fmt.Sprintf(u.t("duration.day"), min/1440)
	}
}

// formatCountdown renders the remaining time for the big countdown block:
// "05:30", "3:15:00" and, from a day on, "2일 3:15:00" (#89).
func (u *ui) formatCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	days, rest := total/86400, total%86400
	hh, mm, ss := rest/3600, rest%3600/60, rest%60
	switch {
	case days > 0:
		return fmt.Sprintf(u.t("schedule.countdown.days"), days,
			fmt.Sprintf("%d:%02d:%02d", hh, mm, ss))
	case hh > 0:
		return fmt.Sprintf("%d:%02d:%02d", hh, mm, ss)
	}
	return fmt.Sprintf("%02d:%02d", mm, ss)
}

// section renders a subtle bold header above content — lighter than
// widget.Card, which draws a large title and a visible card surface.
func section(title string, content fyne.CanvasObject) fyne.CanvasObject {
	head := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return container.NewVBox(head, content)
}

// hint renders wrapped, de-emphasised helper text under a control.
func hint(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	l.Importance = widget.LowImportance
	return l
}

// connState drives the colour of the status dot.
type connState int

const (
	connPending connState = iota // connecting / restarting
	connOK
	connLost
)

// setConn recolours the status dot. Must be called on the UI thread.
func (u *ui) setConn(s connState) {
	if u.statusDot == nil {
		return
	}
	switch s {
	case connOK:
		u.statusDot.FillColor = theme.Color(theme.ColorNameSuccess)
	case connLost:
		u.statusDot.FillColor = theme.Color(theme.ColorNameError)
	default:
		u.statusDot.FillColor = theme.Color(theme.ColorNameDisabled)
	}
	u.statusDot.Refresh()
}

// rebuild recreates the whole window content in the current language.
func (u *ui) rebuild() {
	// The form tabs are rebuilt too; their unsaved edits are carried over
	// as drafts and written into the new widgets below.
	if u.forms == nil {
		u.forms = &forms{}
	}
	if u.deferContent {
		// A minimized start: only the tray until the window is first shown
		// (ensureContent builds the rest). Every setter of a widget creates
		// its renderer and measures text, so nothing else is built yet.
		if u.statusText == "" {
			u.statusText = u.t("status.connecting")
		}
		u.setupTray()
		u.win.SetCloseIntercept(u.onCloseRequest)
		u.refreshTrayStatus()
		u.applyConnected(u.connected.Load())
		return
	}
	drafts := u.forms.drafts()
	u.forms.tabs = nil

	// Only the very first build is "connecting"; on a rebuild (language
	// change) show the known state so the bar doesn't flash back to it.
	statusKey := "status.connecting"
	state := connPending
	if u.connected.Load() {
		statusKey = "status.connected"
		state = connOK
	} else if u.tabs != nil {
		statusKey = "status.unreachable"
		state = connLost
	}
	u.statusText = u.t(statusKey)
	if statusKey == "status.unreachable" {
		u.statusText = fmt.Sprintf(u.statusText, currentWebUIPort())
	}
	u.status = widget.NewLabel(u.statusText)
	// Never let a long status line (e.g. "command sent: …") widen the window.
	u.status.Truncation = fyne.TextTruncateEllipsis
	u.statusDot = canvas.NewCircle(theme.Color(theme.ColorNameDisabled))
	u.setConn(state)
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
		// rebuild() replaces every widget (the forms keep their edits);
		// keep the current tab and reload status/logs/schedule so the
		// window doesn't fall back to "Connecting...". The switch back is
		// not the user leaving a tab, so it asks nothing.
		selected := u.tabs.SelectedIndex()
		u.rebuild()
		if u.connected.Load() && selected >= 0 && selected < len(u.tabs.Items) {
			u.switching = true
			u.tabs.SelectIndex(selected)
			u.switching = false
		}
		go u.initialLoad()
	})
	if u.lang == LangKo {
		langSelect.SetSelected("한국어")
	} else {
		langSelect.SetSelected("English")
	}

	// Shown after the user cancels the login dialog (or a lockout): the
	// only way back into the dialog, so no timer re-opens it (#98).
	u.loginBtn = widget.NewButtonWithIcon(u.t("login.ok"), theme.LoginIcon(), func() {
		u.promptLogin(func() { go u.initialLoad() })
	})
	u.loginBtn.Importance = widget.HighImportance
	if u.loginState() != loginDeferred {
		u.loginBtn.Hide()
	}

	versionLabel := widget.NewLabel(u.version)
	versionLabel.Importance = widget.LowImportance
	u.batteryLabel = widget.NewLabel("")
	u.applyBattery(u.lastBattery)
	// GridWrap pins the circle to 10×10 (a bare canvas object has no
	// minimum size); Center keeps it on the text baseline.
	dot := container.NewCenter(container.NewGridWrap(fyne.NewSize(10, 10), u.statusDot))
	// The status label is the Border's centre object so it takes whatever
	// width is left (and truncates) instead of dictating the window width.
	topBar := container.NewBorder(nil, nil, container.NewPadded(dot), container.NewHBox(u.loginBtn, u.batteryLabel, versionLabel, langSelect), u.status)

	// In the order of the tab* constants (savebar.go). Titles only, no
	// icons: eight tabs with icons need about 640 px in Korean and more
	// than the window's 640 in English; without them both fit (#128).
	u.tabTitles = make([]string, len(tabKeys))
	items := make([]*container.TabItem, len(tabKeys))
	builders := []func() fyne.CanvasObject{
		tabCommands:    u.buildCommandsTab,
		tabSchedule:    u.buildScheduleTab,
		tabPresets:     u.buildPresetsTab,
		tabShare:       u.buildShareTab,
		tabSmartThings: u.buildSmartThingsTab,
		tabTelegram:    u.buildNotifyTab,
		tabSettings:    u.buildSettingsTab,
		tabLogs:        u.buildLogsTab,
	}
	for i, key := range tabKeys {
		u.tabTitles[i] = u.t(key)
		items[i] = container.NewTabItem(u.tabTitles[i], builders[i]())
	}
	u.tabs = container.NewAppTabs(items...)
	u.curTab = tabCommands
	u.shownTab.Store(tabCommands)
	u.tabs.OnSelected = u.onTabSelected

	u.win.SetContent(container.NewBorder(topBar, nil, nil, nil, u.tabs))
	u.setupTray()
	// SetSystemTrayWindow (in setupTray) installs a plain Hide as the
	// close intercept; put the unsaved-changes prompt back in front of it.
	u.win.SetCloseIntercept(u.onCloseRequest)
	u.refreshTrayStatus()
	u.applyConnected(u.connected.Load())

	u.forms.restore(drafts)
	if u.forms.base != nil {
		u.onConfig(*u.forms.base)
	}
	u.applyTrust() // the editors' lock (C5); refreshes the dirty markers too
}

// applyConnected gates UI that needs the service: while unreachable, only
// the Settings tab (install/start lives there) and basic tray entries stay
// usable. Must be called on the UI thread.
func (u *ui) applyConnected(on bool) {
	for _, item := range u.trayNeedsConn {
		item.Disabled = !on
	}
	if u.trayMenu != nil {
		u.trayMenu.Refresh()
	}
	if u.tabs == nil {
		return // only the tray so far (a minimized start)
	}
	// Forced switches, so no unsaved-changes prompt: to the settings tab
	// while the service is gone, and back to where the user was (the
	// commands tab after a start) once it answers. The tab is noted before
	// disabling: AppTabs moves off a tab that gets disabled.
	if !on && u.curTab != tabSettings && u.returnTab == 0 {
		u.returnTab = u.curTab + 1
	}
	u.switching = true
	for i := range u.tabs.Items {
		if i == tabSettings {
			continue
		}
		if on {
			u.tabs.EnableIndex(i)
		} else {
			u.tabs.DisableIndex(i)
		}
	}
	u.switching = false
	switch {
	case !on:
		// The battery reading comes from the service; without it, say nothing.
		u.applyBattery(Battery{})
		u.selectTab(tabSettings)
	case u.returnTab != 0:
		back := u.returnTab - 1
		u.returnTab = 0
		if u.curTab == tabSettings {
			u.selectTab(back)
		}
	}
	if u.settingsExtra != nil {
		if on {
			u.settingsExtra.Show()
		} else {
			u.settingsExtra.Hide()
		}
		if u.settingsRoot != nil {
			u.settingsRoot.Refresh()
		}
	}
}

// --- Data loading / polling ---

func (u *ui) initialLoad() {
	// The local trusted session first, secret or not (C5): the config read
	// without it has the preset paths masked.
	u.ensureLocalSession()
	cfg, err := u.client.GetConfig()
	if err != nil && !errors.Is(err, errUnauthorized) && u.followPortChange() {
		// The service came back on the port saved in the settings (#121).
		u.ensureLocalSession()
		cfg, err = u.client.GetConfig()
	}
	if errors.Is(err, errUnauthorized) && u.tryLocalLogin() {
		// The service vouched for this process (#131); the login dialog
		// below is only for when it does not.
		cfg, err = u.client.GetConfig()
	}
	fyne.Do(func() {
		if errors.Is(err, errUnauthorized) {
			u.connected.Store(false)
			u.setStatus(u.t("login.required"))
			u.setConn(connPending)
			u.applyConnected(false)
			if shouldPromptLogin(u.loginState()) {
				u.promptLogin(func() { go u.initialLoad() })
			}
			return
		}
		if err != nil {
			u.connected.Store(false)
			u.forgetLocalSession()
			u.setStatus(fmt.Sprintf(u.t("status.unreachable"), currentWebUIPort()))
			u.setConn(connLost)
			u.applyConnected(false)
			return
		}
		u.connected.Store(true)
		u.setStatus(u.t("status.connected"))
		u.setConn(connOK)
		u.applyConnected(true)
		// A reconnect or a language switch lands here too: tabs with
		// unsaved edits keep them (compared against the old baseline) and
		// only the others follow the service.
		u.adoptConfig(cfg, u.forms.base)
	})
	if err == nil {
		// The schedule feeds the tray too; the rest only matters on screen
		// and comes with the window otherwise (checkVisible).
		u.loadSchedule()
		if u.onScreen() || windowOnScreen() {
			u.loadShown()
		}
	}
}

// withGraceFallback mirrors what the grace select shows for a service
// predating grace_seconds (enabled, no period), so a config adopted as the
// baseline does not leave the settings form dirty.
func withGraceFallback(cfg Config) Config {
	if cfg.ShutdownGrace && cfg.GraceSeconds <= 0 {
		cfg.GraceSeconds = fallbackGraceSeconds
	}
	return cfg
}

// pollLoop drives periodic refreshes until the window closes.
func (u *ui) pollLoop() {
	logsTick := time.NewTicker(3 * time.Second)
	schedTick := time.NewTicker(2 * time.Second)
	connTick := time.NewTicker(5 * time.Second)
	updateTick := time.NewTicker(24 * time.Hour)
	idleTick := time.NewTicker(idleHeartbeatInterval)
	awakeTick := time.NewTicker(awakePollInterval)
	defer awakeTick.Stop()
	batteryTick := time.NewTicker(batteryPollInterval)
	defer batteryTick.Stop()
	mediaTick := time.NewTicker(mediaWatchInterval)
	defer mediaTick.Stop()
	visTick := time.NewTicker(visibleCheckInterval)
	defer visTick.Stop()
	defer logsTick.Stop()
	defer schedTick.Stop()
	defer connTick.Stop()
	defer updateTick.Stop()
	defer idleTick.Stop()

	for {
		select {
		case <-u.quit:
			return
		case <-updateTick.C:
			if u.app.Preferences().BoolWithFallback("check_updates", true) {
				go u.checkForUpdates(false)
			}
		case <-visTick.C:
			u.checkVisible()
		case <-logsTick.C:
			if u.logsPollWanted() {
				u.loadLogs()
			}
		case <-schedTick.C:
			// Hidden too: the tray entry and tooltip show the countdown,
			// and a new schedule raises the grace toast.
			if u.connected.Load() {
				u.loadSchedule()
			}
		case <-awakeTick.C:
			if u.connected.Load() && u.onScreen() {
				go u.loadAwake()
			}
		case <-batteryTick.C:
			if u.connected.Load() && u.onScreen() {
				go u.loadBattery()
			}
		case <-mediaTick.C:
			// Like the heartbeat, not gated on u.connected: the change
			// check keeps the service current while the window is closed;
			// mediaTick refreshes the card only while it is on screen.
			go u.mediaTick()
		case <-connTick.C:
			// Not while the login dialog is up or an attempt is in
			// flight: a 401 would only race the dialog (#98).
			if shouldAutoLoad(u.connected.Load(), u.loginState()) {
				go u.initialLoad()
			}
		case <-idleTick.C:
			// Not gated on u.connected: the heartbeat is the service's
			// only source of idle time (#77) and must keep flowing while
			// the window is closed, so it makes its own (silent) attempt.
			go u.sendIdleHeartbeat()
		}
	}
}

func (u *ui) refreshNow() {
	go u.loadSchedule()
	go u.loadLogs()
}

// markDisconnectedOnNetError flips the status bar when the service goes away.
func (u *ui) markDisconnectedOnNetError(err error) {
	if errors.Is(err, errUnauthorized) {
		return
	}
	if u.connected.Swap(false) {
		u.forgetLocalSession()
		fyne.Do(func() {
			u.setStatus(fmt.Sprintf(u.t("status.unreachable"), currentWebUIPort()))
			u.setConn(connLost)
			u.applyConnected(false)
		})
	}
}
