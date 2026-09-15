package gui

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// toggle is an on/off switch (a pill with a sliding knob) for settings that
// enable a whole feature — Telegram notifications, remote control, quiet
// hours, browser WebUI, autostart. Fyne 2.8 has no switch widget of its
// own, so this mirrors the parts of widget.Check the tabs rely on: Checked,
// SetChecked (which fires OnChanged, like Check), Enable/Disable.
// Sub-options that are one of several flags stay checkboxes.
type toggle struct {
	widget.DisableableWidget
	Text      string
	Checked   bool
	OnChanged func(bool)
}

const (
	toggleTrackW = 40
	toggleTrackH = 22
	toggleKnob   = 16
	toggleAnim   = 140 * time.Millisecond
)

func newToggle(text string, onChanged func(bool)) *toggle {
	t := &toggle{Text: text, OnChanged: onChanged}
	t.ExtendBaseWidget(t)
	return t
}

// SetChecked sets the state, redraws and fires OnChanged when it changed.
func (t *toggle) SetChecked(on bool) {
	if on == t.Checked {
		return
	}
	t.Checked = on
	t.Refresh()
	if t.OnChanged != nil {
		t.OnChanged(on)
	}
}

// Tapped flips the switch unless disabled.
func (t *toggle) Tapped(*fyne.PointEvent) {
	if t.Disabled() {
		return
	}
	t.SetChecked(!t.Checked)
}

func (t *toggle) CreateRenderer() fyne.WidgetRenderer {
	track := canvas.NewRectangle(color.Transparent)
	track.CornerRadius = toggleTrackH / 2
	knob := canvas.NewCircle(color.Transparent)
	label := canvas.NewText(t.Text, color.Black)
	label.TextSize = theme.TextSize()
	r := &toggleRenderer{t: t, track: track, knob: knob, label: label}
	r.Refresh()
	return r
}

type toggleRenderer struct {
	t     *toggle
	track *canvas.Rectangle
	knob  *canvas.Circle
	label *canvas.Text
	anim  *fyne.Animation
	// knobOn is the state the knob was last laid out for, so Layout can
	// tell a pure resize from a state change (only the latter animates).
	knobOn bool
}

func (r *toggleRenderer) Destroy() {
	if r.anim != nil {
		r.anim.Stop()
	}
}

func (r *toggleRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.knob, r.label}
}

func (r *toggleRenderer) MinSize() fyne.Size {
	pad := theme.Padding()
	text := r.label.MinSize()
	w := toggleTrackW + pad*2 + text.Width
	h := max(float32(toggleTrackH), text.Height) + pad
	return fyne.NewSize(w, h)
}

// knobPos returns the knob's top-left for the given state within size.
func (r *toggleRenderer) knobPos(on bool, size fyne.Size) fyne.Position {
	y := (size.Height - toggleKnob) / 2
	if on {
		return fyne.NewPos(toggleTrackW-toggleKnob-3, y)
	}
	return fyne.NewPos(3, y)
}

func (r *toggleRenderer) Layout(size fyne.Size) {
	pad := theme.Padding()
	r.track.Resize(fyne.NewSize(toggleTrackW, toggleTrackH))
	r.track.Move(fyne.NewPos(0, (size.Height-toggleTrackH)/2))
	r.knob.Resize(fyne.NewSize(toggleKnob, toggleKnob))

	target := r.knobPos(r.t.Checked, size)
	if r.knobOn != r.t.Checked && r.knob.Position() != target && r.knob.Size().Width > 0 {
		// State change: slide the knob across.
		if r.anim != nil {
			r.anim.Stop()
		}
		r.anim = canvas.NewPositionAnimation(r.knob.Position(), target, toggleAnim, r.knob.Move)
		r.anim.Curve = fyne.AnimationEaseOut
		r.anim.Start()
	} else {
		r.knob.Move(target)
	}
	r.knobOn = r.t.Checked

	text := r.label.MinSize()
	r.label.Move(fyne.NewPos(toggleTrackW+pad*2, (size.Height-text.Height)/2))
	r.label.Resize(fyne.NewSize(size.Width-toggleTrackW-pad*2, text.Height))
}

func (r *toggleRenderer) Refresh() {
	th := r.t.Theme()
	v := fyne.CurrentApp().Settings().ThemeVariant()
	r.label.Text = r.t.Text
	r.label.TextSize = th.Size(theme.SizeNameText)
	switch {
	case r.t.Disabled():
		r.track.FillColor = th.Color(theme.ColorNameDisabled, v)
		r.knob.FillColor = th.Color(theme.ColorNameBackground, v)
		r.label.Color = th.Color(theme.ColorNameDisabled, v)
	case r.t.Checked:
		r.track.FillColor = th.Color(theme.ColorNamePrimary, v)
		r.knob.FillColor = th.Color(theme.ColorNameForegroundOnPrimary, v)
		r.label.Color = th.Color(theme.ColorNameForeground, v)
	default:
		r.track.FillColor = th.Color(theme.ColorNameInputBorder, v)
		r.knob.FillColor = th.Color(theme.ColorNameForeground, v)
		r.label.Color = th.Color(theme.ColorNameForeground, v)
	}
	r.Layout(r.t.Size())
	r.track.Refresh()
	r.knob.Refresh()
	r.label.Refresh()
}
