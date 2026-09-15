package gui

import (
	"fmt"
	"image/color"
	"net/url"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The notifications tab (issue #64, design doc §12): Telegram connection,
// inbound control, the event catalogue, quiet hours and display options.
// The service owns delivery; this tab only edits the telegram/notify parts
// of the config and offers the test/lookup helpers of /api/telegram/*.

// maskedTokenPrefix is how GET /api/config hands the bot token to the GUI
// ("****6789"); sending it back unchanged keeps the stored token.
const maskedTokenPrefix = "****"

// botFatherURL is the hint hyperlink of the connection section.
const botFatherURL = "https://core.telegram.org/bots#how-do-i-create-a-bot"

// notifyKind is one toggleable Category.Kind with its catalogue default —
// the fallback when a (pre-catalogue) service omits the key.
type notifyKind struct {
	kind string
	on   bool
}

// notifyCatalogue lists the categories in display order with every
// user-toggleable kind (design doc §3). system.test and system.digest are
// deliberately absent: they are not user-toggleable.
var notifyCatalogue = []struct {
	cat   string
	kinds []notifyKind
}{
	{"remote", []notifyKind{{"received", true}, {"grace_scheduled", true}, {"grace_cancelled", true}, {"executed", true}, {"force", true}}},
	{"schedule", []notifyKind{{"created", false}, {"cancelled", false}, {"executed", true}, {"replaced", true}}},
	{"power", []notifyKind{{"started", true}, {"resumed", true}, {"stopping", false}}},
	{"security", []notifyKind{{"unauthorized", true}, {"login_limited", true}, {"unknown_command", true}, {"config_changed", true}, {"unknown_chat", true}}},
	{"system", []notifyKind{{"update_available", true}, {"updated", true}, {"exec_failed", true}, {"tray_wake_failed", true}}},
}

// detailOptions maps the detail select's index to the wire value.
var detailOptions = []string{"simple", "full"}

const (
	defaultDetail     = "full"
	defaultQuietStart = "22:00"
	defaultQuietEnd   = "07:00"
)

// --- Pure form model (unit-tested) -----------------------------------------

// notifyFormState is the tab's contents as plain values, independent of
// widgets, so the config round-trip and the dirty check are testable.
type notifyFormState struct {
	// Token is the entry text: "" or the masked value mean "keep the
	// stored token", "-" clears it, anything else is a new plaintext token.
	Token          string
	ChatID         string
	Enabled        bool
	ControlEnabled bool
	AllowedChatIDs string // comma-separated entry text
	// Masters is the per-category master check; Kinds the child checks as
	// displayed. A kind is effectively on only when its master is on too
	// (children keep their values while the master is off, see
	// effectiveNotify).
	Masters map[string]bool
	Kinds   map[string]map[string]bool
	Quiet   QuietHours
	Detail  string // "simple" | "full"
	PCName  string
}

// normalizeTelegram fills the string defaults a client or an older
// service may leave empty, so the form and the dirty check see one shape.
func normalizeTelegram(tg TelegramConfig) TelegramConfig {
	if tg.Detail != "simple" && tg.Detail != "full" {
		tg.Detail = defaultDetail
	}
	if tg.QuietHours.Start == "" {
		tg.QuietHours.Start = defaultQuietStart
	}
	if tg.QuietHours.End == "" {
		tg.QuietHours.End = defaultQuietEnd
	}
	return tg
}

// notifyValue reports whether cat.kind is on in cfg, falling back to the
// catalogue default when the key is missing.
func notifyValue(cfg Config, cat, kind string) bool {
	if kinds, ok := cfg.Notify[cat]; ok {
		if v, ok := kinds[kind]; ok {
			return v
		}
	}
	for _, c := range notifyCatalogue {
		if c.cat != cat {
			continue
		}
		for _, k := range c.kinds {
			if k.kind == kind {
				return k.on
			}
		}
	}
	return false
}

// anyOn reports whether at least one value is true.
func anyOn(m map[string]bool) bool {
	for _, v := range m {
		if v {
			return true
		}
	}
	return false
}

// notifyStateFromConfig is what the tab shows for cfg: the master of a
// category is on when any of its kinds is.
func notifyStateFromConfig(cfg Config) notifyFormState {
	tg := normalizeTelegram(cfg.Telegram)
	s := notifyFormState{
		Token:          tg.BotToken,
		ChatID:         tg.ChatID,
		Enabled:        tg.Enabled,
		ControlEnabled: tg.ControlEnabled,
		AllowedChatIDs: strings.Join(tg.AllowedChatIDs, ", "),
		Masters:        map[string]bool{},
		Kinds:          map[string]map[string]bool{},
		Quiet:          tg.QuietHours,
		Detail:         tg.Detail,
		PCName:         tg.PCName,
	}
	for _, c := range notifyCatalogue {
		kinds := make(map[string]bool, len(c.kinds))
		for _, k := range c.kinds {
			kinds[k.kind] = notifyValue(cfg, c.cat, k.kind)
		}
		s.Kinds[c.cat] = kinds
		s.Masters[c.cat] = anyOn(kinds)
	}
	return s
}

// effective reports the value that will be saved for cat.kind: the child
// value gated by its master.
func (s notifyFormState) effective(cat, kind string) bool {
	return s.Masters[cat] && s.Kinds[cat][kind]
}

// effectiveNotify returns a fresh map: base's entries (so keys the GUI does
// not know survive) with every catalogue kind overwritten by the form.
func (s notifyFormState) effectiveNotify(base map[string]map[string]bool) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(notifyCatalogue))
	for cat, kinds := range base {
		out[cat] = make(map[string]bool, len(kinds))
		for kind, on := range kinds {
			out[cat][kind] = on
		}
	}
	for _, c := range notifyCatalogue {
		if out[c.cat] == nil {
			out[c.cat] = make(map[string]bool, len(c.kinds))
		}
		for _, k := range c.kinds {
			out[c.cat][k.kind] = s.effective(c.cat, k.kind)
		}
	}
	return out
}

// tokenChanged reports whether the token entry holds a new token: anything
// non-empty that is neither the masked value nor another "****" form (the
// service keeps the stored token for those, so they are not a change).
func tokenChanged(typed, masked string) bool {
	return typed != "" && typed != masked && !strings.HasPrefix(typed, maskedTokenPrefix)
}

// tokenToSend is the bot_token for POST /api/config: an emptied entry
// sends the masked value back (keep), otherwise the text as typed — the
// service applies the keep/clear/replace rules itself.
func tokenToSend(typed, masked string) string {
	if typed == "" {
		return masked
	}
	return typed
}

// maskToken mirrors the service's masking ("****" + last 4, or "****" for
// short tokens) for the local fallback when the post-save re-fetch fails.
func maskToken(plain string) string {
	if plain == "" {
		return ""
	}
	r := []rune(plain)
	if len(r) < 8 {
		return maskedTokenPrefix
	}
	return maskedTokenPrefix + string(r[len(r)-4:])
}

// maskedAfterSave is the masked token the baseline should hold after a
// save of typed when the service could not be re-read: a kept token keeps
// its old mask, "-" clears, a new token is masked locally.
func maskedAfterSave(typed, oldMasked string) string {
	switch {
	case typed == "" || strings.HasPrefix(typed, maskedTokenPrefix):
		return oldMasked
	case typed == "-":
		return ""
	default:
		return maskToken(typed)
	}
}

// parseChatIDs splits the comma-separated entry, trimming blanks and
// dropping empties. Never nil, so the JSON is [] (nil would mean "keep
// current" to the service).
func parseChatIDs(text string) []string {
	out := []string{}
	for _, part := range strings.Split(text, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// applyTo returns base with the form's fields written over telegram/notify.
// Everything else (port, secret, grace…) is untouched so a save from this
// tab never clobbers the settings tab; the message language follows the
// app language (design doc §10). base's maps and slices are not aliased.
func (s notifyFormState) applyTo(base Config, appLang Lang) Config {
	cfg := base
	tg := normalizeTelegram(base.Telegram)
	tg.BotToken = tokenToSend(s.Token, base.Telegram.BotToken)
	tg.Enabled = s.Enabled
	tg.ChatID = strings.TrimSpace(s.ChatID)
	tg.ControlEnabled = s.ControlEnabled
	tg.AllowedChatIDs = parseChatIDs(s.AllowedChatIDs)
	tg.Detail = s.Detail
	tg.PCName = strings.TrimSpace(s.PCName)
	tg.QuietHours = s.Quiet
	tg.Lang = string(appLang)
	cfg.Telegram = tg
	cfg.Notify = s.effectiveNotify(base.Notify)
	return cfg
}

// dirty reports whether saving would change base. The language is not
// compared (it is written silently on save) and a masked/empty token is
// "unchanged".
func (s notifyFormState) dirty(base Config) bool {
	if tokenChanged(s.Token, base.Telegram.BotToken) {
		return true
	}
	tg := normalizeTelegram(base.Telegram)
	if s.Enabled != tg.Enabled ||
		strings.TrimSpace(s.ChatID) != tg.ChatID ||
		s.ControlEnabled != tg.ControlEnabled ||
		!slices.Equal(parseChatIDs(s.AllowedChatIDs), tg.AllowedChatIDs) ||
		s.Quiet != tg.QuietHours ||
		s.Detail != tg.Detail ||
		strings.TrimSpace(s.PCName) != tg.PCName {
		return true
	}
	for _, c := range notifyCatalogue {
		for _, k := range c.kinds {
			if s.effective(c.cat, k.kind) != notifyValue(base, c.cat, k.kind) {
				return true
			}
		}
	}
	return false
}

// quietHourOptions is "00:00" … "23:30" in 30-minute steps.
func quietHourOptions() []string {
	out := make([]string, 0, 48)
	for h := 0; h < 24; h++ {
		out = append(out, fmt.Sprintf("%02d:00", h), fmt.Sprintf("%02d:30", h))
	}
	return out
}

// chatLabel renders one /api/telegram/chats entry for the picker:
// "title (@username) · chat_id".
func chatLabel(c TelegramChat) string {
	title := c.Title
	if title == "" {
		title = c.Type
	}
	if c.Username != "" {
		title += " (@" + c.Username + ")"
	}
	return title + " · " + c.ChatID
}

// --- Widgets -----------------------------------------------------------------

// notifyTab holds the tab's widgets; rebuilt with the window on a language
// change, filled by fillNotifyTab.
type notifyTab struct {
	root *fyne.Container

	tokenEntry   *widget.Entry
	chatEntry    *widget.Entry
	enabledCheck *toggle
	botStatus    *widget.Label // "@bot" from getMe, or why not
	testStatus   *widget.Label // last [Send test] result
	testBtn      *widget.Button
	findBtn      *widget.Button

	controlCheck *toggle
	allowedEntry *widget.Entry
	controlWarn  *widget.Label

	masters map[string]*widget.Check
	kinds   map[string]map[string]*widget.Check

	quietCheck    *toggle
	quietStart    *widget.Select
	quietEnd      *widget.Select
	quietSecurity *widget.Check
	quietDigest   *widget.Check

	detailSelect *widget.Select
	pcNameEntry  *widget.Entry

	bar *saveBar
	// filling suppresses the OnChanged cascade while fillNotifyTab writes
	// the widgets; the dirty state is evaluated once at the end.
	filling bool
}

// state reads the widgets into the pure form model.
func (t *notifyTab) state() notifyFormState {
	s := notifyFormState{
		Token:          t.tokenEntry.Text,
		ChatID:         t.chatEntry.Text,
		Enabled:        t.enabledCheck.Checked,
		ControlEnabled: t.controlCheck.Checked,
		AllowedChatIDs: t.allowedEntry.Text,
		Masters:        map[string]bool{},
		Kinds:          map[string]map[string]bool{},
		Quiet: QuietHours{
			Enabled:        t.quietCheck.Checked,
			Start:          t.quietStart.Selected,
			End:            t.quietEnd.Selected,
			SecurityBypass: t.quietSecurity.Checked,
			Digest:         t.quietDigest.Checked,
		},
		Detail: defaultDetail,
		PCName: t.pcNameEntry.Text,
	}
	if i := t.detailSelect.SelectedIndex(); i >= 0 && i < len(detailOptions) {
		s.Detail = detailOptions[i]
	}
	for cat, m := range t.masters {
		s.Masters[cat] = m.Checked
		s.Kinds[cat] = map[string]bool{}
		for kind, c := range t.kinds[cat] {
			s.Kinds[cat][kind] = c.Checked
		}
	}
	return s
}

// setChildrenEnabled enables the kind checks of cat to match its master.
func (t *notifyTab) setChildrenEnabled(cat string, on bool) {
	for _, c := range t.kinds[cat] {
		if on {
			c.Enable()
		} else {
			c.Disable()
		}
	}
}

// setQuietEnabled greys the quiet-hours controls while the feature is off.
func (t *notifyTab) setQuietEnabled(on bool) {
	for _, w := range []fyne.Disableable{t.quietStart, t.quietEnd, t.quietSecurity, t.quietDigest} {
		if on {
			w.Enable()
		} else {
			w.Disable()
		}
	}
}

// setControlWarn shows the warning line only while control is enabled and
// re-lays out the tab so the rows below move (hidden objects take no room).
func (t *notifyTab) setControlWarn(on bool) {
	if on {
		t.controlWarn.Show()
	} else {
		t.controlWarn.Hide()
	}
	if t.root != nil {
		t.root.Refresh()
	}
}

// indent is a fixed-width transparent spacer used to nest the kind checks
// under their category master.
func indent(width float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(width, 1))
	return r
}

func (u *ui) buildNotifyTab() fyne.CanvasObject {
	t := &notifyTab{
		masters: map[string]*widget.Check{},
		kinds:   map[string]map[string]*widget.Check{},
	}
	u.notify = t
	onEdit := func(string) { u.updateNotifySaveState() }
	onToggle := func(bool) { u.updateNotifySaveState() }

	// 1. Telegram connection --------------------------------------------------
	t.tokenEntry = widget.NewPasswordEntry()
	t.tokenEntry.SetPlaceHolder(u.t("notify.token.placeholder"))
	t.tokenEntry.OnChanged = onEdit
	t.chatEntry = widget.NewEntry()
	t.chatEntry.OnChanged = onEdit
	t.enabledCheck = newToggle(u.t("notify.enabled"), onToggle)
	t.botStatus = hint(u.t("notify.bot.none"))
	t.testStatus = widget.NewLabel("")
	t.testStatus.Wrapping = fyne.TextWrapWord

	t.findBtn = widget.NewButtonWithIcon(u.t("notify.chatid.find"), theme.SearchIcon(), func() { u.findChatID() })
	t.testBtn = widget.NewButtonWithIcon(u.t("notify.test"), theme.MailSendIcon(), func() { u.sendTelegramTest() })

	link, _ := url.Parse(botFatherURL)
	hyperlink := widget.NewHyperlink(u.t("notify.telegram.link"), link)
	hyperlink.Wrapping = fyne.TextWrapWord

	telegramBody := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem(u.t("notify.token"), t.tokenEntry),
			widget.NewFormItem(u.t("notify.chatid"), t.chatEntry),
		),
		container.NewHBox(t.findBtn, t.testBtn, layout.NewSpacer()),
		t.testStatus,
		t.enabledCheck,
		t.botStatus,
		hint(u.t("notify.telegram.hint")),
		hyperlink,
	)

	// 2. Control from Telegram -------------------------------------------------
	t.controlWarn = widget.NewLabel(u.t("notify.control.warn"))
	t.controlWarn.Wrapping = fyne.TextWrapWord
	t.controlWarn.Importance = widget.WarningImportance
	t.controlWarn.Hide()
	t.controlCheck = newToggle(u.t("notify.control.enabled"), func(on bool) {
		t.setControlWarn(on)
		u.updateNotifySaveState()
	})
	t.allowedEntry = widget.NewEntry()
	t.allowedEntry.SetPlaceHolder("123456789, -1001234567890")
	t.allowedEntry.OnChanged = onEdit

	controlBody := container.NewVBox(
		t.controlCheck,
		t.controlWarn,
		widget.NewForm(widget.NewFormItem(u.t("notify.control.allowed"), t.allowedEntry)),
		hint(u.t("notify.control.allowed.hint")),
		hint(u.t("notify.control.hint")),
	)

	// 3. Events ----------------------------------------------------------------
	setAll := func(on bool) {
		t.filling = true
		for cat, m := range t.masters {
			for _, c := range t.kinds[cat] {
				c.SetChecked(on)
			}
			m.SetChecked(on)
			t.setChildrenEnabled(cat, on)
		}
		t.filling = false
		u.updateNotifySaveState()
	}
	allOn := widget.NewButton(u.t("notify.all.on"), func() { setAll(true) })
	allOff := widget.NewButton(u.t("notify.all.off"), func() { setAll(false) })
	eventsBody := container.NewVBox(container.NewHBox(layout.NewSpacer(), allOn, allOff))

	for _, c := range notifyCatalogue {
		cat := c.cat
		children := make([]*widget.Check, 0, len(c.kinds))
		t.kinds[cat] = map[string]*widget.Check{}
		master := widget.NewCheck(u.t("notify.cat."+cat), nil)
		t.masters[cat] = master
		master.OnChanged = func(on bool) {
			if t.filling {
				return
			}
			// Turning a category on with every kind off would derive the
			// master straight back off; start with everything on instead.
			if on {
				t.filling = true
				if !anyChecked(children) {
					for _, ch := range children {
						ch.SetChecked(true)
					}
				}
				t.filling = false
			}
			t.setChildrenEnabled(cat, on)
			u.updateNotifySaveState()
		}
		for _, k := range c.kinds {
			child := widget.NewCheck(u.t("notify.kind."+cat+"."+k.kind), nil)
			child.OnChanged = func(bool) {
				if t.filling {
					return
				}
				// The master derives from the children: the last one off
				// takes the category off (and greys the children).
				if on := anyChecked(children); master.Checked != on {
					master.SetChecked(on) // runs master.OnChanged
					return
				}
				u.updateNotifySaveState()
			}
			t.kinds[cat][k.kind] = child
			children = append(children, child)
		}
		grid := make([]fyne.CanvasObject, len(children))
		for i, ch := range children {
			grid[i] = ch
		}
		eventsBody.Add(master)
		// Two columns keep the list short; the indent nests them visually.
		eventsBody.Add(container.NewBorder(nil, nil, indent(24), nil, container.NewGridWithColumns(2, grid...)))
	}

	// 4. Quiet hours -----------------------------------------------------------
	t.quietStart = widget.NewSelect(quietHourOptions(), onEdit)
	t.quietEnd = widget.NewSelect(quietHourOptions(), onEdit)
	t.quietSecurity = widget.NewCheck(u.t("notify.quiet.security"), onToggle)
	t.quietDigest = widget.NewCheck(u.t("notify.quiet.digest"), onToggle)
	t.quietCheck = newToggle(u.t("notify.quiet.enabled"), func(on bool) {
		t.setQuietEnabled(on)
		u.updateNotifySaveState()
	})
	quietBody := container.NewVBox(
		t.quietCheck,
		container.NewHBox(indent(24), widget.NewLabel(u.t("notify.quiet.range")), t.quietStart, widget.NewLabel("~"), t.quietEnd, layout.NewSpacer()),
		t.quietSecurity,
		t.quietDigest,
		hint(u.t("notify.quiet.hint")),
	)

	// 5. Display ---------------------------------------------------------------
	t.detailSelect = widget.NewSelect([]string{u.t("notify.detail.simple"), u.t("notify.detail.full")}, onEdit)
	t.pcNameEntry = widget.NewEntry()
	t.pcNameEntry.SetPlaceHolder(u.t("notify.pcname.placeholder"))
	t.pcNameEntry.OnChanged = onEdit
	displayBody := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem(u.t("notify.detail"), t.detailSelect),
			widget.NewFormItem(u.t("notify.pcname"), t.pcNameEntry),
		),
		hint(u.t("notify.display.hint")),
	)

	// Save lives in the fixed footer (savebar.go), enabled only while the
	// form differs from cfgBaseline (nil until initialLoad fills the tab).
	t.bar = newSaveBar(u, func() { u.saveNotifyTab(false) })

	t.root = container.NewVBox(
		section(u.t("notify.telegram"), telegramBody),
		widget.NewSeparator(),
		section(u.t("notify.control"), controlBody),
		widget.NewSeparator(),
		section(u.t("notify.events"), eventsBody),
		widget.NewSeparator(),
		section(u.t("notify.quiet"), quietBody),
		widget.NewSeparator(),
		section(u.t("notify.display"), displayBody),
		// Trailing padding so the last row never sits flush against the
		// footer (same as the settings tab).
		widget.NewLabel(""),
	)
	return withSaveBar(t.root, t.bar)
}

// anyChecked reports whether any of the checks is on.
func anyChecked(checks []*widget.Check) bool {
	for _, c := range checks {
		if c.Checked {
			return true
		}
	}
	return false
}

// selectTime points sel at value, appending it as an extra option when it
// is not on the 30-minute grid (set through the API) so saving other
// fields does not silently change it.
func selectTime(sel *widget.Select, value string) {
	if !slices.Contains(sel.Options, value) {
		sel.Options = append(sel.Options, value)
	}
	sel.SetSelected(value)
}

// fillNotifyTab writes cfg into the tab and re-evaluates Save. Must be
// called on the UI thread, after cfgBaseline is set.
func (u *ui) fillNotifyTab(cfg Config) {
	t := u.notify
	if t == nil {
		return
	}
	s := notifyStateFromConfig(cfg)
	t.filling = true
	t.tokenEntry.SetText(s.Token)
	t.chatEntry.SetText(s.ChatID)
	t.enabledCheck.SetChecked(s.Enabled)
	t.controlCheck.SetChecked(s.ControlEnabled)
	t.setControlWarn(s.ControlEnabled)
	t.allowedEntry.SetText(s.AllowedChatIDs)
	for cat, kinds := range t.kinds {
		for kind, c := range kinds {
			c.SetChecked(s.Kinds[cat][kind])
		}
		t.masters[cat].SetChecked(s.Masters[cat])
		t.setChildrenEnabled(cat, s.Masters[cat])
	}
	t.quietCheck.SetChecked(s.Quiet.Enabled)
	selectTime(t.quietStart, s.Quiet.Start)
	selectTime(t.quietEnd, s.Quiet.End)
	t.quietSecurity.SetChecked(s.Quiet.SecurityBypass)
	t.quietDigest.SetChecked(s.Quiet.Digest)
	t.setQuietEnabled(s.Quiet.Enabled)
	t.detailSelect.SetSelectedIndex(slices.Index(detailOptions, s.Detail))
	t.pcNameEntry.SetText(s.PCName)
	t.testStatus.SetText("")
	t.filling = false
	u.updateNotifySaveState()

	// Connection status: ask the service which bot the stored token belongs
	// to. Low importance — informational, and the error text may be long.
	if !cfg.Telegram.BotTokenSet {
		t.botStatus.SetText(u.t("notify.bot.none"))
		return
	}
	t.botStatus.SetText(u.t("notify.bot.checking"))
	status := t.botStatus
	go func() {
		username, _, err := u.client.TelegramMe()
		fyne.Do(func() {
			if err != nil {
				status.SetText(fmt.Sprintf(u.t("notify.bot.error"), err.Error()))
				return
			}
			status.SetText(fmt.Sprintf(u.t("notify.bot.connected"), username))
		})
	}()
}

// notifyDirty reports whether the notify tab differs from cfgBaseline.
// False while the tab is being filled or before a baseline exists.
func (u *ui) notifyDirty() bool {
	t := u.notify
	if t == nil || t.bar == nil || t.filling || u.cfgBaseline == nil {
		return false
	}
	return t.state().dirty(*u.cfgBaseline)
}

// updateNotifySaveState enables the tab's Save, the pulsing indicator and
// the tab marker only while the form differs from cfgBaseline. Safe to call
// before the tab exists. UI thread only.
func (u *ui) updateNotifySaveState() {
	t := u.notify
	if t == nil || t.bar == nil || t.filling {
		return
	}
	dirty := u.notifyDirty()
	t.bar.setDirty(dirty)
	u.markTab(tabNotify, dirty)
}

// saveNotifyTab posts the baseline with this tab's fields written over it,
// then re-reads the config so the baseline (and the masked token) reflect
// what the service stored. quiet skips the "Saved" dialog. Returns false
// when the save failed. UI thread only.
func (u *ui) saveNotifyTab(quiet bool) bool {
	t := u.notify
	if t == nil || u.cfgBaseline == nil {
		return false
	}
	s := t.state()
	cfg := s.applyTo(*u.cfgBaseline, u.lang)
	msg, err := u.client.SaveConfig(cfg)
	if err != nil {
		dialog.ShowError(err, u.win)
		return false
	}
	fresh, err := u.client.GetConfig()
	if err != nil {
		// Saved, but unreadable right now: adopt what was sent, with the
		// token re-masked so the form does not stay dirty.
		fresh = cfg
		fresh.Telegram.BotToken = maskedAfterSave(s.Token, u.cfgBaseline.Telegram.BotToken)
		fresh.Telegram.BotTokenSet = fresh.Telegram.BotToken != ""
	}
	fresh = withGraceFallback(fresh)
	u.cfgBaseline = &fresh
	u.fillNotifyTab(fresh)
	// The settings tab compares against the same baseline.
	u.updateSaveState()
	if !quiet {
		dialog.ShowInformation(u.t("settings.saved"), msg, u.win)
	}
	return true
}

// sendTelegramTest asks the service to send one test message with the
// form's current token/chat id (unsaved values are fine; the service falls
// back to the stored token for an empty or masked one).
func (u *ui) sendTelegramTest() {
	t := u.notify
	if t == nil {
		return
	}
	t.testBtn.Disable()
	t.testStatus.Importance = widget.LowImportance
	t.testStatus.SetText(u.t("notify.test.sending"))
	token, chatID := t.tokenEntry.Text, strings.TrimSpace(t.chatEntry.Text)
	go func() {
		err := u.client.TestTelegram(token, chatID)
		fyne.Do(func() {
			t.testBtn.Enable()
			if err != nil {
				t.testStatus.Importance = widget.DangerImportance
				t.testStatus.SetText(err.Error())
			} else {
				t.testStatus.Importance = widget.SuccessImportance
				t.testStatus.SetText(u.t("notify.test.sent"))
			}
			t.testStatus.Refresh()
		})
	}()
}

// findChatID lists the chats that recently wrote to the bot and fills the
// Chat ID entry with the one picked.
func (u *ui) findChatID() {
	t := u.notify
	if t == nil {
		return
	}
	t.findBtn.Disable()
	token := t.tokenEntry.Text
	go func() {
		chats, err := u.client.TelegramChats(token)
		fyne.Do(func() {
			t.findBtn.Enable()
			if err != nil {
				dialog.ShowError(err, u.win)
				return
			}
			if len(chats) == 0 {
				// Wrapped body: ShowInformation's label would widen the
				// dialog to the full sentence.
				body := widget.NewLabel(u.t("notify.chatid.none"))
				body.Wrapping = fyne.TextWrapWord
				d := dialog.NewCustom(u.t("notify.chatid.pick"), u.t("login.cancel"), body, u.win)
				d.Resize(fyne.NewSize(420, 0))
				d.Show()
				return
			}
			// A List sizes to the dialog instead of growing to its longest
			// row, so a long group title cannot widen the window.
			list := widget.NewList(
				func() int { return len(chats) },
				func() fyne.CanvasObject {
					l := widget.NewLabel("")
					l.Truncation = fyne.TextTruncateEllipsis
					return l
				},
				func(i widget.ListItemID, o fyne.CanvasObject) {
					o.(*widget.Label).SetText(chatLabel(chats[i]))
				},
			)
			d := dialog.NewCustom(u.t("notify.chatid.pick"), u.t("login.cancel"), list, u.win)
			list.OnSelected = func(id widget.ListItemID) {
				t.chatEntry.SetText(chats[id].ChatID)
				d.Hide()
			}
			d.Resize(fyne.NewSize(440, 320))
			d.Show()
		})
	}()
}
