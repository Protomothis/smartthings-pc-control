package gui

// Output and input device slots (#145, #159).
//
// The features tab's 오디오 장치 칸 row shows what the service assigned —
// read-only, nothing here picks or switches a device:
//
//	출력 장치 칸
//	칸 1  스피커 (Realtek(R) Audio)
//	칸 2  헤드셋 (WH-1000XM4) (연결 안 됨)
//	칸 3  (비어 있음)
//	…
//	[새로 고침] [초기화]
//
// The service fills the slots itself: a device it sees for the first time
// takes the lowest free slot and keeps it, connected or not. SmartThings
// routines compare against the slot number ("current output is slot 1").
// 초기화 (POST /api/audio/slots/reset, after a confirmation) empties both
// lists, and the devices connected at that moment fill them again. "(연결
// 안 됨)" comes from reading the devices in-process — this app already
// runs in the user's session.

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
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

func audioSlotsFromConfig(cfg Config) audioSlotsState {
	return audioSlotsState{Outputs: slotArray(cfg.Audio.Outputs), Inputs: slotArray(cfg.Audio.Inputs)}
}

// slotLine is one row of a list: the device name, "(비어 있음)" for an
// empty slot, and "(연결 안 됨)" after a device that is not connected now.
// known is false before the devices were read: then nothing is marked.
func slotLine(l Lang, name string, connected []string, known bool) string {
	switch {
	case name == "":
		return T(l, "audio.slots.empty")
	case known && !slices.ContainsFunc(connected, func(d string) bool { return strings.EqualFold(strings.TrimSpace(d), name) }):
		return name + " " + T(l, "audio.slots.missing")
	}
	return name
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

// readAudioDevices reads the devices for "(연결 안 됨)"; replaced by the
// tests (no sound card needed).
var readAudioDevices = useraction.ReadAudioDevices

// audioSlotList is one direction's five read-only rows.
type audioSlotList struct {
	vals    [audioSlotCount]string
	devices []string
	known   bool
	labels  [audioSlotCount]*widget.Label
}

// audioSlotsSection is the features tab's two lists.
type audioSlotsSection struct {
	out, in *audioSlotList
	status  *widget.Label
	refresh *widget.Button
	reset   *widget.Button
}

// buildAudioSlotsSection creates both lists; the devices are read in the
// background.
func (u *ui) buildAudioSlotsSection() fyne.CanvasObject {
	s := &audioSlotsSection{out: &audioSlotList{}, in: &audioSlotList{}}
	if old := u.audioSlots; old != nil {
		// A rebuild (language change) keeps what was shown.
		*s.out, *s.in = *old.out, *old.in
	}
	u.audioSlots = s
	s.status = hint("")
	s.refresh = widget.NewButtonWithIcon(u.t("audio.slots.refresh"), theme.ViewRefreshIcon(), func() { u.loadAudioSlots() })
	s.reset = widget.NewButtonWithIcon(u.t("audio.slots.reset"), theme.ContentClearIcon(), func() { u.confirmAudioSlotsReset() })
	body := container.NewVBox(
		widget.NewLabelWithStyle(u.t("audio.slots.outputs"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		u.slotRows(s.out),
		widget.NewLabelWithStyle(u.t("audio.slots.inputs"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		u.slotRows(s.in),
		hint(u.t("audio.slots.hint")),
		container.NewHBox(s.refresh, s.reset, layout.NewSpacer()),
		s.status,
	)
	u.loadAudioDevices()
	return body
}

// slotRows lays out a list: "칸 N" and the name.
func (u *ui) slotRows(l *audioSlotList) fyne.CanvasObject {
	rows := container.NewVBox()
	for i := range audioSlotCount {
		l.labels[i] = widget.NewLabel("")
		l.labels[i].Truncation = fyne.TextTruncateEllipsis
		label := widget.NewLabel(fmt.Sprintf(u.t("audio.slots.slot"), i+1))
		rows.Add(container.NewBorder(nil, nil, label, nil, l.labels[i]))
	}
	l.render(u.lang)
	return rows
}

// render writes vals into the rows. UI thread only.
func (l *audioSlotList) render(lang Lang) {
	for i, w := range l.labels {
		if w != nil {
			w.SetText(slotLine(lang, l.vals[i], l.devices, l.known))
		}
	}
}

// showAudioSlots puts slots into both lists. UI thread only.
func (u *ui) showAudioSlots(st audioSlotsState) {
	s := u.audioSlots
	if s == nil {
		return
	}
	s.out.vals, s.in.vals = st.Outputs, st.Inputs
	s.out.render(u.lang)
	s.in.render(u.lang)
	u.refreshFeatureHeads()
}

// loadAudioDevices reads the connected devices for "(연결 안 됨)".
func (u *ui) loadAudioDevices() {
	s := u.audioSlots
	if s == nil {
		return
	}
	type lists struct{ out, in []string }
	runAsync(nil, func() (lists, error) {
		outputs, inputs, err := readAudioDevices()
		return lists{endpointNames(outputs), endpointNames(inputs)}, err
	}, func(l lists, err error) {
		if err != nil {
			s.status.SetText(fmt.Sprintf(u.t("audio.slots.readfailed"), err))
			return
		}
		s.status.SetText("")
		s.out.devices, s.in.devices = l.out, l.in
		s.out.known, s.in.known = true, true
		s.out.render(u.lang)
		s.in.render(u.lang)
	})
}

// loadAudioSlots is the 새로 고침 button: the slots as the service has
// them now (it may have assigned some since the config was loaded) and
// the connected devices.
func (u *ui) loadAudioSlots() {
	s := u.audioSlots
	if s == nil {
		return
	}
	runAsync(busyControls(s.refresh, s.reset), func() (Config, error) { return u.client.GetConfig() },
		func(cfg Config, err error) {
			if err != nil {
				s.status.SetText(fmt.Sprintf(u.t("audio.slots.readfailed"), err))
				return
			}
			u.adoptAudioSlots(cfg.Audio)
			u.loadAudioDevices()
		})
}

// confirmAudioSlotsReset asks, then empties the slots.
func (u *ui) confirmAudioSlotsReset() {
	dialog.ShowConfirm(u.t("audio.slots.reset"), u.t("audio.slots.reset.confirm"), func(ok bool) {
		if ok {
			u.resetAudioSlots()
		}
	}, u.win)
}

// resetAudioSlots runs the reset and shows the slots the service filled
// again.
func (u *ui) resetAudioSlots() {
	s := u.audioSlots
	if s == nil {
		return
	}
	runAsync(busyControls(s.refresh, s.reset), func() (AudioConfig, error) { return u.client.ResetAudioSlots() },
		func(slots AudioConfig, err error) {
			if err != nil {
				s.status.SetText(fmt.Sprintf(u.t("audio.slots.resetfailed"), err))
				return
			}
			s.status.SetText("")
			u.adoptAudioSlots(slots)
			u.loadAudioDevices()
		})
}

// adoptAudioSlots makes slots the saved ones: the baseline (the slots are
// no form field, so no tab turns dirty) and the lists. UI thread only.
func (u *ui) adoptAudioSlots(slots AudioConfig) {
	if u.forms != nil && u.forms.base != nil {
		u.forms.base.Audio = slots
	}
	u.showAudioSlots(audioSlotsFromConfig(Config{Audio: slots}))
}

// fillAudioSlots writes cfg into the lists. UI thread only.
func (u *ui) fillAudioSlots(cfg Config) {
	u.showAudioSlots(audioSlotsFromConfig(cfg))
}

// audioSlotsFormState is what the lists show; the zero state before they
// exist. Only the features tab's head reads it: the slots are not saved
// with the tab.
func (u *ui) audioSlotsFormState() audioSlotsState {
	s := u.audioSlots
	if s == nil {
		return audioSlotsState{}
	}
	return audioSlotsState{Outputs: s.out.vals, Inputs: s.in.vals}
}

// errAudioSlotsUnsupported is a service without the reset (before #159).
var errAudioSlotsUnsupported = errors.New("the service is older than v1.3.0")

// --- Heartbeat -----------------------------------------------------------------

// sampleAudioDevices is the heartbeat's device block: names and the
// default, nil when the devices cannot be read.
func sampleAudioDevices(sampledAt string) *HeartbeatAudioDevices {
	outputs, inputs, err := heartbeatDevices()
	if err != nil {
		return nil
	}
	return &HeartbeatAudioDevices{Outputs: outputs, Inputs: inputs, SampledAt: sampledAt}
}

// heartbeatDevices reads the device lists for the heartbeat; the tests
// replace it.
var heartbeatDevices = useraction.ReadAudioDevices

// AudioConfig mirrors service.AudioConfig (#145): device names by slot.
// The service fills them and ignores them in a POST (#159).
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
