package gui

// The logs tab: the service log, filtered, refreshed while it is shown.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (u *ui) buildLogsTab() fyne.CanvasObject {
	u.logsLabel = widget.NewLabel(u.t("logs.empty"))
	u.logsLabel.Wrapping = fyne.TextWrapBreak
	u.logsLabel.TextStyle = fyne.TextStyle{Monospace: true}
	u.logsScroll = container.NewVScroll(u.logsLabel)

	u.logsAuto = widget.NewCheck(u.t("logs.autorefresh"), func(on bool) { u.logsAutoOn.Store(on) })
	u.logsAuto.SetChecked(true)
	u.logsAutoOn.Store(true)
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
	if u.logsLabel == nil {
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
