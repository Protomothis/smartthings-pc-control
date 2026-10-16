package gui

// Camera/microphone detection (#143, docs/design/v1.3-sensors.md) on the
// sharing tab (share_tab.go): the opt-in switch, what a remote or
// scheduled shutdown does while either is in use, and one line with the
// state the service sees right now. It is part of the tab's form
// (shareFormState) and saves with it.

import (
	"encoding/json"
	"fmt"
	"net/http"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// PrivacyConfig mirrors service config "privacy": guard is off, warn or
// block. A missing guard (an older service) reads as warn.
type PrivacyConfig struct {
	Enabled bool   `json:"enabled"`
	Guard   string `json:"guard"`
}

// PrivacyBlock is the status privacy block: in_use, idle or unknown per
// device.
type PrivacyBlock struct {
	Camera     string `json:"camera"`
	Microphone string `json:"microphone"`
	Since      string `json:"since"`
}

// PrivacyState is GET /api/privacy; Privacy is nil while detection is off.
type PrivacyState struct {
	Enabled bool          `json:"enabled"`
	Guard   string        `json:"guard"`
	Privacy *PrivacyBlock `json:"privacy"`
}

// privacyAPI is the one call the section makes; *Client has it.
type privacyAPI interface {
	GetPrivacy() (PrivacyState, error)
}

var _ privacyAPI = (*Client)(nil)

// GetPrivacy returns the service's camera/microphone state. An older
// service (no such route) reads as detection off.
func (c *Client) GetPrivacy() (PrivacyState, error) {
	var p PrivacyState
	resp, err := c.do("GET", "/api/privacy", nil)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return p, json.NewDecoder(resp.Body).Decode(&p)
	case http.StatusUnauthorized:
		return p, errUnauthorized
	case http.StatusNotFound, http.StatusForbidden:
		return p, nil
	}
	return p, fmt.Errorf("HTTP %d", resp.StatusCode)
}

// --- Pure form model (unit-tested) -----------------------------------------

// privacyGuards are the guard values in the select's order.
var privacyGuards = []string{"off", "warn", "block"}

// normalizePrivacy fills a missing or unknown guard with warn, the
// service's default.
func normalizePrivacy(p PrivacyConfig) PrivacyConfig {
	for _, g := range privacyGuards {
		if p.Guard == g {
			return p
		}
	}
	p.Guard = "warn"
	return p
}

// privacyEqual compares two settings after normalisation.
func privacyEqual(a, b PrivacyConfig) bool { return normalizePrivacy(a) == normalizePrivacy(b) }

// privacyGuardIndex is the select index of a guard.
func privacyGuardIndex(guard string) int {
	g := normalizePrivacy(PrivacyConfig{Guard: guard}).Guard
	for i, v := range privacyGuards {
		if v == g {
			return i
		}
	}
	return 1
}

// privacyStateText is the status line: "지금: 카메라 사용 중 · 마이크 쉬는
// 중", or "지금: 감지 꺼짐".
func privacyStateText(l Lang, s PrivacyState) string {
	if !s.Enabled || s.Privacy == nil {
		return fmt.Sprintf(T(l, "privacy.state"), T(l, "privacy.state.off"))
	}
	word := func(state string) string {
		switch state {
		case "in_use":
			return T(l, "privacy.in_use")
		case "idle":
			return T(l, "privacy.idle")
		}
		return T(l, "privacy.unknown")
	}
	line := T(l, "privacy.camera") + " " + word(s.Privacy.Camera) + " · " +
		T(l, "privacy.microphone") + " " + word(s.Privacy.Microphone)
	return fmt.Sprintf(T(l, "privacy.state"), line)
}

// scheduleInUseText is what the grace toast adds for a waiting schedule:
// "\n⚠ 카메라·마이크 사용 중", plus a second line once the guard pushed
// it back. "" when nothing is in use and it was not held.
func scheduleInUseText(l Lang, s Schedule) string {
	out := ""
	if len(s.InUse) > 0 {
		names := ""
		for i, d := range s.InUse {
			if i > 0 {
				names += "·"
			}
			switch d {
			case "camera", "microphone":
				names += T(l, "privacy."+d)
			default:
				names += d
			}
		}
		out += "\n" + fmt.Sprintf(T(l, "schedule.inuse"), names)
	}
	if s.Held {
		out += "\n" + T(l, "schedule.held")
	}
	return out
}

// scheduleInUseNote is scheduleInUseText in the app's language.
func (u *ui) scheduleInUseNote(s Schedule) string { return scheduleInUseText(u.lang, s) }

// --- Widgets -----------------------------------------------------------------

// privacyBox is the section's widgets.
type privacyBox struct {
	enabled *toggle
	guard   *widget.Select
	state   *widget.Label
}

// form reads the widgets.
func (b *privacyBox) form() PrivacyConfig {
	p := PrivacyConfig{Enabled: b.enabled.Checked, Guard: "warn"}
	if i := b.guard.SelectedIndex(); i >= 0 && i < len(privacyGuards) {
		p.Guard = privacyGuards[i]
	}
	return p
}

// buildPrivacyBox builds the section; onEdit marks the tab dirty.
func (u *ui) buildPrivacyBox(onEdit func()) (*privacyBox, fyne.CanvasObject) {
	b := &privacyBox{}
	b.enabled = newToggle(u.t("privacy.enabled"), func(on bool) {
		setEnabled(b.guard, on)
		onEdit()
	})
	labels := make([]string, len(privacyGuards))
	for i, g := range privacyGuards {
		labels[i] = u.t("privacy.guard." + g)
	}
	b.guard = widget.NewSelect(labels, func(string) { onEdit() })
	b.guard.SetSelectedIndex(privacyGuardIndex("warn"))
	b.guard.Disable()
	b.state = widget.NewLabel(fmt.Sprintf(u.t("privacy.state"), u.t("privacy.state.off")))
	box := container.NewVBox(
		b.enabled,
		hint(u.t("privacy.hint")),
		container.NewBorder(nil, nil, widget.NewLabel(u.t("privacy.guard")), nil, b.guard),
		hint(u.t("privacy.guard.hint")),
		b.state,
	)
	return b, box
}

// fill writes p into the widgets. UI thread only.
func (b *privacyBox) fill(p PrivacyConfig) {
	p = normalizePrivacy(p)
	b.enabled.SetChecked(p.Enabled)
	b.guard.SetSelectedIndex(privacyGuardIndex(p.Guard))
	setEnabled(b.guard, p.Enabled)
}

// loadPrivacy refreshes the status line. Runs off the UI thread; an error
// leaves the line as it is.
func (u *ui) loadPrivacy() {
	// Asked for separately from serviceAPI, so the window tests' fakes
	// (which embed that interface) need nothing new.
	api, ok := u.client.(privacyAPI)
	if !ok {
		return
	}
	s, err := api.GetPrivacy()
	if err != nil {
		return
	}
	fyne.Do(func() {
		if u.share != nil && u.share.privacy != nil {
			u.share.privacy.state.SetText(privacyStateText(u.lang, s))
		}
	})
}
