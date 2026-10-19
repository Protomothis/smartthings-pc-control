package gui

// Output and input device slots (#145).
//
// The features tab's 오디오 장치 칸 row (#157) holds two lists of five slots:
//
//	출력 장치 칸
//	칸 1  [스피커 (Realtek(R) Audio)        ▾]  [↑] [↓]
//	칸 2  [(비어 있음)                        ▾]  [↑] [↓]
//	…
//	[장치 목록 새로 고침]
//
// Each select offers the devices connected now — read in-process, this
// app already runs in the user's session — plus the saved name when that
// device is not connected ("… (연결 안 됨)"), so opening the tab never
// drops a slot. The arrows swap a slot with its neighbour: the slot
// number is what SmartThings and Telegram switch by. The slots are saved
// with the rest of the tab (featuresFormState.Audio).
//
// The command tab's media card gets a picker of the filled output slots,
// the default one selected; choosing one sends audio_output.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// audioSlotCount is the number of slots per direction (config.AudioSlots).
const audioSlotCount = 5

// --- Pure model (unit-tested) ------------------------------------------------

// audioSlotsState is both slot lists as plain values: index 0 is slot 1,
// "" an empty slot.
type audioSlotsState struct {
	Outputs [audioSlotCount]string
	Inputs  [audioSlotCount]string
}

func slotArray(names []string) [audioSlotCount]string {
	var out [audioSlotCount]string
	for i, n := range names {
		if i == audioSlotCount {
			break
		}
		out[i] = strings.TrimSpace(n)
	}
	return out
}

// slotList is the config form: trailing empty slots dropped, never nil
// (the service reads a missing list as "keep the stored one").
func slotList(a [audioSlotCount]string) []string {
	out := []string{}
	for _, n := range a {
		out = append(out, strings.TrimSpace(n))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func audioSlotsFromConfig(cfg Config) audioSlotsState {
	return audioSlotsState{Outputs: slotArray(cfg.Audio.Outputs), Inputs: slotArray(cfg.Audio.Inputs)}
}

// applyTo writes the slots into cfg.
func (s audioSlotsState) applyTo(cfg *Config) {
	cfg.Audio.Outputs = slotList(s.Outputs)
	cfg.Audio.Inputs = slotList(s.Inputs)
}

// dirty reports whether saving would change base.
func (s audioSlotsState) dirty(base Config) bool {
	return s != audioSlotsFromConfig(base)
}

// moveSlot swaps slot i with its neighbour in direction dir (-1 up, +1
// down); out of range it changes nothing.
func moveSlot(a [audioSlotCount]string, i, dir int) [audioSlotCount]string {
	j := i + dir
	if i < 0 || i >= audioSlotCount || j < 0 || j >= audioSlotCount {
		return a
	}
	a[i], a[j] = a[j], a[i]
	return a
}

// slotChoices is what one slot's select offers: "" (empty) first, the
// connected devices, and the saved name when it is not among them.
func slotChoices(devices []string, saved string) []string {
	out := []string{""}
	for _, d := range devices {
		if d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	if saved != "" && !slices.ContainsFunc(out, func(d string) bool { return strings.EqualFold(d, saved) }) {
		out = append(out, saved)
	}
	return out
}

// endpointNames is the names of a device list.
func endpointNames(list []useraction.AudioEndpoint) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Name)
	}
	return out
}

// --- Features tab widgets ----------------------------------------------------------

// readAudioDevices reads the devices for the pickers; replaced by the
// tests (no sound card needed, nothing switched either way).
var readAudioDevices = useraction.ReadAudioDevices

// audioSlotEditor is one direction's five rows.
type audioSlotEditor struct {
	vals    [audioSlotCount]string
	devices []string
	selects [audioSlotCount]*widget.Select
	choices [audioSlotCount][]string
	syncing bool
}

// audioSlotsSection is the features tab's two editors.
type audioSlotsSection struct {
	out, in *audioSlotEditor
	status  *widget.Label
	refresh *widget.Button
}

// buildAudioSlotsSection creates both editors; the devices are read in
// the background.
func (u *ui) buildAudioSlotsSection() fyne.CanvasObject {
	s := &audioSlotsSection{out: &audioSlotEditor{}, in: &audioSlotEditor{}}
	u.audioSlots = s
	s.status = hint("")
	s.refresh = widget.NewButtonWithIcon(u.t("audio.slots.refresh"), theme.ViewRefreshIcon(), func() { u.loadAudioDevices() })
	body := container.NewVBox(
		widget.NewLabelWithStyle(u.t("audio.slots.outputs"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		u.slotRows(s.out),
		widget.NewLabelWithStyle(u.t("audio.slots.inputs"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		u.slotRows(s.in),
		hint(u.t("audio.slots.hint")),
		container.NewHBox(s.refresh, layout.NewSpacer()),
		s.status,
	)
	u.loadAudioDevices()
	return body
}

// slotRows lays out an editor: "칸 N", the select, ↑ and ↓.
func (u *ui) slotRows(e *audioSlotEditor) fyne.CanvasObject {
	rows := container.NewVBox()
	for i := range audioSlotCount {
		sel := widget.NewSelect(nil, nil)
		sel.OnChanged = func(string) {
			if e.syncing {
				return
			}
			if k := sel.SelectedIndex(); k >= 0 && k < len(e.choices[i]) {
				e.vals[i] = e.choices[i][k]
			}
			u.refreshDirty()
		}
		e.selects[i] = sel
		up := widget.NewButtonWithIcon("", theme.MoveUpIcon(), func() { e.move(u, i, -1) })
		down := widget.NewButtonWithIcon("", theme.MoveDownIcon(), func() { e.move(u, i, +1) })
		if i == 0 {
			up.Disable()
		}
		if i == audioSlotCount-1 {
			down.Disable()
		}
		label := widget.NewLabel(fmt.Sprintf(u.t("audio.slots.slot"), i+1))
		rows.Add(container.NewBorder(nil, nil, label, container.NewHBox(up, down), sel))
	}
	e.render(u)
	return rows
}

func (e *audioSlotEditor) move(u *ui, i, dir int) {
	e.vals = moveSlot(e.vals, i, dir)
	e.render(u)
	u.refreshDirty()
}

// render puts vals into the selects. UI thread only.
func (e *audioSlotEditor) render(u *ui) {
	e.syncing = true
	defer func() { e.syncing = false }()
	for i, sel := range e.selects {
		if sel == nil {
			continue
		}
		e.choices[i] = slotChoices(e.devices, e.vals[i])
		labels := make([]string, len(e.choices[i]))
		for k, c := range e.choices[i] {
			switch {
			case c == "":
				labels[k] = u.t("audio.slots.empty")
			case !slices.Contains(e.devices, c):
				labels[k] = c + " " + u.t("audio.slots.missing")
			default:
				labels[k] = c
			}
		}
		sel.Options = labels
		k := slices.IndexFunc(e.choices[i], func(c string) bool { return strings.EqualFold(c, e.vals[i]) })
		if k < 0 {
			k = 0
		}
		sel.SetSelectedIndex(k)
		sel.Refresh()
	}
}

// loadAudioDevices reads the connected devices and refreshes the choices.
func (u *ui) loadAudioDevices() {
	s := u.audioSlots
	if s == nil {
		return
	}
	type lists struct{ out, in []string }
	runAsync(busyControls(s.refresh), func() (lists, error) {
		outputs, inputs, err := readAudioDevices()
		return lists{endpointNames(outputs), endpointNames(inputs)}, err
	}, func(l lists, err error) {
		if err != nil {
			s.status.SetText(fmt.Sprintf(u.t("audio.slots.readfailed"), err))
			return
		}
		s.status.SetText("")
		s.out.devices, s.in.devices = l.out, l.in
		s.out.render(u)
		s.in.render(u)
	})
}

// fillAudioSlots writes cfg into the editors. UI thread only.
func (u *ui) fillAudioSlots(cfg Config) {
	s := u.audioSlots
	if s == nil {
		return
	}
	st := audioSlotsFromConfig(cfg)
	s.out.vals, s.in.vals = st.Outputs, st.Inputs
	s.out.render(u)
	s.in.render(u)
}

// audioSlotsFormState reads the editors; the zero state before they exist.
func (u *ui) audioSlotsFormState() audioSlotsState {
	s := u.audioSlots
	if s == nil {
		return audioSlotsState{}
	}
	return audioSlotsState{Outputs: s.out.vals, Inputs: s.in.vals}
}

// --- Media card picker -----------------------------------------------------------

// outputPickerOptions is the media card picker: "1 · 스피커" per filled
// output slot, "(연결 안 됨)" on an absent one, and the index of the default
// slot (-1 when none is).
func (u *ui) outputPickerOptions(slots []AudioSlotInfo) (labels []string, selected int) {
	selected = -1
	for i, s := range slots {
		label := strconv.Itoa(s.Slot) + " · " + s.Name
		if !s.Present {
			label += " " + u.t("audio.slots.missing")
		}
		if s.Default {
			selected = i
		}
		labels = append(labels, label)
	}
	return labels, selected
}

// buildOutputPicker creates the media card's output select.
func (u *ui) buildOutputPicker(c *mediaCard) fyne.CanvasObject {
	c.output = widget.NewSelect(nil, func(string) {
		if c.syncing {
			return
		}
		k := c.output.SelectedIndex()
		if k < 0 || k >= len(c.state.Audio.Outputs) {
			return
		}
		slot := c.state.Audio.Outputs[k].Slot
		u.sendMediaCommand("audio_output", &slot)
	})
	c.output.PlaceHolder = u.t("audio.output.pick")
	c.outputRow = container.NewBorder(nil, nil, widget.NewLabel(u.t("audio.output.label")), nil, c.output)
	c.outputRow.Hide()
	return c.outputRow
}

// applyOutputPicker mirrors the output slots; hidden while there are none.
// Called from applyMedia with c.syncing set.
func (u *ui) applyOutputPicker(c *mediaCard, m MediaState, usable bool) {
	if c.output == nil {
		return
	}
	if len(m.Audio.Outputs) == 0 {
		c.outputRow.Hide()
		return
	}
	labels, sel := u.outputPickerOptions(m.Audio.Outputs)
	c.output.Options = labels
	if sel >= 0 {
		c.output.SetSelectedIndex(sel)
	} else {
		c.output.ClearSelected()
	}
	c.output.Refresh()
	setEnabled(c.output, usable && m.Audio.Available)
	c.outputRow.Show()
}

// audioSwitchErrorText words a refused audio_output, "" for other codes.
func (u *ui) audioSwitchErrorText(ce *mediaCommandError) string {
	switch ce.Code {
	case "slot_empty":
		return u.t("audio.err.slot_empty")
	case "device_missing":
		return fmt.Sprintf(u.t("audio.err.device_missing"), ce.Message)
	case "switch_failed":
		return fmt.Sprintf(u.t("audio.err.switch_failed"), ce.Message)
	}
	return ""
}

// sampleAudioDevices is the heartbeat's device block: names and the
// default only, nil when the devices cannot be read.
func sampleAudioDevices(sampledAt string) *HeartbeatAudioDevices {
	outputs, inputs, err := heartbeatDevices()
	if err != nil {
		return nil
	}
	strip := func(l []useraction.AudioEndpoint) []useraction.AudioEndpoint {
		out := make([]useraction.AudioEndpoint, 0, len(l))
		for _, e := range l {
			out = append(out, useraction.AudioEndpoint{Name: e.Name, Default: e.Default})
		}
		return out
	}
	return &HeartbeatAudioDevices{Outputs: strip(outputs), Inputs: strip(inputs), SampledAt: sampledAt}
}

// heartbeatDevices reads the device lists for the heartbeat; the tests
// replace it.
var heartbeatDevices = useraction.ReadAudioDevices

// AudioConfig mirrors service.AudioConfig (#145): device names by slot.
// The lists travel as [] when emptied — a missing list keeps the stored one.
type AudioConfig struct {
	Outputs []string `json:"outputs"`
	Inputs  []string `json:"inputs"`
}

// AudioSlotInfo is one slot of the status audio block.
type AudioSlotInfo struct {
	Slot    int    `json:"slot"`
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Default bool   `json:"default"`
}

// HeartbeatAudioDevices is the heartbeat's device block (#145).
type HeartbeatAudioDevices struct {
	Outputs   []useraction.AudioEndpoint `json:"outputs"`
	Inputs    []useraction.AudioEndpoint `json:"inputs"`
	SampledAt string                     `json:"sampled_at"`
}
