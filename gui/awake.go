package gui

// Keep-awake row on the command tab (#111): a toggle, how long, and what is
// left. The state lives in the service (GET/POST/DELETE /api/awake); this
// row only mirrors it, so SmartThings and Telegram changes show up on the
// next poll.

import (
	"errors"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// awakePresets are the durations the row offers, in minutes; 0 is "until
// turned off". The service accepts 0–1440.
var awakePresets = []int{30, 60, 120, 240, 0}

// defaultAwakePreset is preselected until the service's default_minutes is
// known (and when that is not one of the presets).
const defaultAwakePreset = 60

// awakePollInterval refreshes the row. The remaining time is shown in
// minutes, so this is plenty.
const awakePollInterval = 10 * time.Second

// awakeRow is the widgets and the last known state.
type awakeRow struct {
	toggle *toggle
	sel    *widget.Select
	status *widget.Label
	// syncing is set while the row is updated from the service, so the
	// widgets' change callbacks do not send the state straight back.
	syncing bool
	// preset is true once the select followed default_minutes.
	preset bool
}

// awakePresetIndex is the position of minutes in awakePresets, or -1.
func awakePresetIndex(minutes int) int {
	for i, m := range awakePresets {
		if m == minutes {
			return i
		}
	}
	return -1
}

// awakePresetLabel names one preset: "30분", "1시간", "끌 때까지".
func (u *ui) awakePresetLabel(minutes int) string {
	if minutes == 0 {
		return u.t("awake.forever")
	}
	return u.formatMinutes(minutes)
}

// awakeStatusText is the line next to the toggle: "42분 남음 · 14:30까지",
// "켜짐 · 끌 때까지" or "꺼짐 — …".
func (u *ui) awakeStatusText(a Awake, now time.Time) string {
	if !a.On {
		return u.t("awake.off")
	}
	until, err := time.Parse(time.RFC3339, a.Until)
	if a.Until == "" || err != nil {
		return u.t("awake.on.forever")
	}
	until = until.In(now.Location())
	// Round up: "1분 남음" until the very end, never "0분".
	left := (a.RemainingSeconds + 59) / 60
	if left < 1 {
		left = 1
	}
	at := until.Format("15:04")
	if y, m, d := until.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
		at = fmt.Sprintf(u.t("awake.tomorrow"), at)
	}
	return fmt.Sprintf(u.t("awake.left"), u.formatMinutes(left), at)
}

// buildAwakeRow creates the row for the command tab.
func (u *ui) buildAwakeRow() fyne.CanvasObject {
	row := &awakeRow{}
	u.awake = row

	labels := make([]string, len(awakePresets))
	for i, m := range awakePresets {
		labels[i] = u.awakePresetLabel(m)
	}
	row.sel = widget.NewSelect(labels, func(string) {
		// The select fires OnChanged from SetSelectedIndex below, before the
		// toggle exists; a nil toggle is "not on", so there is nothing to send.
		if row.syncing || row.toggle == nil || !row.toggle.Checked {
			return
		}
		// A new duration while on starts a new period from now.
		u.sendAwake(true)
	})
	row.sel.SetSelectedIndex(awakePresetIndex(defaultAwakePreset))

	row.toggle = newToggle(u.t("awake.use"), func(on bool) {
		if row.syncing {
			return
		}
		u.sendAwake(on)
	})
	row.status = widget.NewLabel("")
	row.status.Importance = widget.LowImportance
	row.status.Truncation = fyne.TextTruncateEllipsis

	// A titled card like 일반 / 전원 / 미디어 / 프리셋; the title names the
	// feature, so the toggle itself only says "사용".
	return section(u.t("awake.toggle"), container.NewVBox(
		container.NewBorder(nil, nil, container.NewHBox(row.toggle, row.sel), nil, row.status),
		hint(u.t("awake.hint")),
	))
}

// selectedAwakeMinutes is the duration the select shows.
func (u *ui) selectedAwakeMinutes() int {
	if u.awake == nil {
		return defaultAwakePreset
	}
	if i := u.awake.sel.SelectedIndex(); i >= 0 && i < len(awakePresets) {
		return awakePresets[i]
	}
	return defaultAwakePreset
}

// sendAwake turns keep-awake on (for the selected duration) or off. UI
// goroutine only: the select is read here, and the request runs through
// runAsync with the row busy.
func (u *ui) sendAwake(on bool) {
	minutes := u.selectedAwakeMinutes()
	var busy func(bool)
	if row := u.awake; row != nil {
		busy = busyControls(row.toggle, row.sel)
	}
	runAsync(busy, func() (Awake, error) {
		if on {
			return u.client.SetAwake(minutes)
		}
		return u.client.AwakeOff()
	}, func(a Awake, err error) {
		if err != nil {
			if errors.Is(err, errAwakeUnsupported) {
				err = errors.New(u.t("awake.unsupported"))
			}
			dialog.ShowError(err, u.win)
			// Put the toggle back to whatever the service says.
			background(u.loadAwake)
			return
		}
		u.applyAwake(a)
	})
}

// loadAwake polls the service. Runs off the UI thread, so u.awake (rebuilt
// on a language change) is only looked at inside the fyne.Do callbacks.
func (u *ui) loadAwake() {
	a, err := u.client.GetAwake()
	if err != nil {
		if errors.Is(err, errAwakeUnsupported) {
			fyne.Do(func() {
				if u.awake == nil {
					return
				}
				u.awake.toggle.Disable()
				u.awake.sel.Disable()
				u.awake.status.SetText(u.t("awake.unsupported"))
			})
			return
		}
		u.markDisconnectedOnNetError(err)
		return
	}
	fyne.Do(func() { u.applyAwake(a) })
}

// applyAwake mirrors a to the row. Must be called on the UI thread.
func (u *ui) applyAwake(a Awake) {
	row := u.awake
	if row == nil {
		return
	}
	row.syncing = true
	defer func() { row.syncing = false }()
	row.toggle.Enable()
	row.sel.Enable()
	if row.toggle.Checked != a.On {
		row.toggle.SetChecked(a.On)
	}
	if !row.preset && !a.On {
		// Follow awake.default_minutes once, while nothing is running.
		if i := awakePresetIndex(a.DefaultMinutes); i >= 0 {
			row.sel.SetSelectedIndex(i)
		}
		row.preset = true
	}
	row.status.SetText(u.awakeStatusText(a, time.Now()))
}
