package gui

// The logs tab: the service log, filtered, refreshed while it is shown.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/internal/systool"
)

// logsBottomSlack is how far above the last line (in px) the view still
// counts as sitting at the bottom: float rounding, not a user's scroll.
const logsBottomSlack = 1

func (u *ui) buildLogsTab() fyne.CanvasObject {
	// One paragraph per line, coloured by logSegments; wrapped like the
	// label it replaced (#134).
	u.logsText = widget.NewRichText()
	u.logsText.Wrapping = fyne.TextWrapBreak
	u.logsShown, u.logsShownMsg = nil, ""
	u.setLogsMessage("logs.empty")
	u.logsScroll = container.NewVScroll(u.logsText)
	u.logsScroll.OnScrolled = u.onLogsScrolled
	u.logsOffsetY = 0
	u.logsScrolling = false

	// Checked before the callback goes in, so building the tab does not
	// count as the user turning auto refresh on.
	u.logsAuto = widget.NewCheck(u.t("logs.autorefresh"), nil)
	u.logsAuto.SetChecked(true)
	u.logsAutoOn.Store(true)
	u.logsAuto.OnChanged = u.onLogsAutoChanged
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

	// Toolbar on one row, filter on its own row: a single row of five
	// controls plus the entry would push the window's minimum width past
	// its default size (and differently per language, see #53).
	toolbar := container.NewHBox(u.logsAuto, refreshBtn, layout.NewSpacer(), openFileBtn, openDirBtn)
	top := container.NewVBox(toolbar, u.logsFilter)
	return container.NewBorder(top, nil, nil, nil, u.logsScroll)
}

// serviceLogPath is service.log next to the exe — the same location the
// service uses.
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
		cmd = exec.Command(systool.Explorer())
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: fmt.Sprintf(`explorer.exe /select,"%s"`, path)}
	} else {
		// `start "" <file>` opens with the file's associated app; hide the
		// helper console since the GUI is built with -H=windowsgui.
		cmd = systool.Command(systool.Cmd, "/c", "start", "", path)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
	if err := cmd.Start(); err != nil {
		dialog.ShowError(err, u.win)
	}
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

// renderLogs shows the cached lines that match the filter. The view is
// rebuilt only when what it shows changes (an unchanged poll every 3 s
// would otherwise redraw the whole view), and it follows the newest line
// while auto refresh is on; with it off, only a view already at the end
// stays there. Must be called on the UI thread.
func (u *ui) renderLogs() {
	if u.logsText == nil {
		return // not built yet (a minimized start)
	}
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
	follow := u.logsAuto.Checked || u.logsAtBottom(u.logsScroll.Offset.Y)
	switch {
	case len(shown) > 0:
		u.setLogsLines(shown)
	case filter != "" && len(u.logLines) > 0:
		u.setLogsMessage("logs.nomatch")
	default:
		u.setLogsMessage("logs.empty")
	}
	if follow {
		u.logsToBottom()
	}
}

// setLogsLines puts lines in the view unless it already shows exactly
// those. UI goroutine only.
func (u *ui) setLogsLines(lines []string) {
	if u.logsShownMsg == "" && u.logsShown != nil && slices.Equal(u.logsShown, lines) {
		return
	}
	u.logsShown, u.logsShownMsg = lines, ""
	u.logsText.Segments = logRichSegments(lines)
	u.logsText.Refresh()
}

// setLogsMessage shows the locale message key (logs.empty, logs.nomatch)
// dimmed in place of the lines, unless it is already shown. UI goroutine
// only.
func (u *ui) setLogsMessage(key string) {
	if u.logsShownMsg == key {
		return
	}
	u.logsShown, u.logsShownMsg = nil, key
	u.logsText.Segments = []widget.RichTextSegment{logTextSegment(u.t(key), theme.ColorNamePlaceHolder, false)}
	u.logsText.Refresh()
}

// logsAtBottom reports whether offset y shows the end of the view (or
// the whole of it, when it fits). Measured on the content's MinSize, as
// Fyne's Scroll clamps its offset.
func (u *ui) logsAtBottom(y float32) bool {
	s := u.logsScroll
	if s.Content.Size().Height <= s.Size().Height {
		return true // Scroll keeps the offset at 0 then
	}
	return y >= s.Content.MinSize().Height-s.Size().Height-logsBottomSlack
}

// logsToBottom scrolls the view to the newest line, marked as our own
// scroll so onLogsScrolled does not take it for the user's. A no-op while
// the view has no size yet (the tab never shown): setCurTab calls it again
// once the tab is in front and laid out. UI goroutine only.
func (u *ui) logsToBottom() {
	s := u.logsScroll
	if s == nil || s.Size().Height <= 0 {
		return
	}
	u.logsScrolling = true
	defer func() { u.logsScrolling = false }()
	// Lay the content out first. After new text, or on the first show of
	// the tab, the content still has the size of the last layout until the
	// next frame: Scroll.ScrollToBottom measures the distance on
	// Content.MinSize but its updateOffset gives up (offset 0) while the
	// stale Content.Size fits the view. Resizing the RichText to the new
	// width can re-wrap it and so change its MinSize again; a few rounds
	// settle it (Fyne's scroll layout: max of MinSize and the view).
	c := s.Content
	for range 3 {
		ms := c.MinSize()
		want := fyne.NewSize(fyne.Max(ms.Width, s.Size().Width), fyne.Max(ms.Height, s.Size().Height))
		if c.Size() == want {
			break
		}
		c.Resize(want)
	}
	bottom := fyne.Max(0, c.Size().Height-s.Size().Height)
	if s.Offset.Y != bottom {
		s.ScrollToBottom()
	}
	u.logsOffsetY = s.Offset.Y
}

// onLogsScrolled is the view's OnScrolled. Fyne calls it whenever the
// offset changes: a wheel or touchpad scroll (Scroll.Scrolled), a drag of
// or tap on the scroll bar, our ScrollToBottom, and the clamping a resize
// or refresh does (refreshBars). Ours are marked by logsScrolling; a clamp
// always lands on the bottom. So a move up that does not end at the bottom
// is the user's, and while auto refresh is on it turns auto refresh off,
// through the check like a click (logsAutoOn follows, the poll pauses).
// Moving down, or anywhere while auto refresh is off, changes nothing.
func (u *ui) onLogsScrolled(p fyne.Position) {
	prev := u.logsOffsetY
	u.logsOffsetY = p.Y
	if u.logsScrolling || u.logsAuto == nil || !u.logsAuto.Checked {
		return
	}
	if p.Y < prev && !u.logsAtBottom(p.Y) {
		u.logsAuto.SetChecked(false)
	}
}

// onLogsAutoChanged is the auto-refresh check's callback: pollLoop's flag
// follows it, and turning it on jumps to the newest line and refreshes at
// once instead of at the next 3 s tick.
func (u *ui) onLogsAutoChanged(on bool) {
	u.logsAutoOn.Store(on)
	if !on {
		return
	}
	u.logsToBottom()
	if u.connected.Load() {
		background(u.loadLogs)
	}
}

// logsShownAgain puts the view back on the newest line when the logs tab
// comes to the front with auto refresh on. AppTabs lays the tab out before
// it reports the selection, so the view has its size by now; logsToBottom
// settles the content's. UI goroutine only.
func (u *ui) logsShownAgain() {
	if u.logsAuto != nil && u.logsAuto.Checked {
		u.logsToBottom()
	}
}
