package service

// Output and input device slots (#145, #159).
//
// The status audio block lists up to five output and five input slots
// (config audio.outputs / audio.inputs, device names by slot) with present
// and default, so a SmartThings routine can ask "is the current output
// slot 2". Nothing switches devices any more (#159); the slots only
// report.
//
// The slots fill themselves: a device seen for the first time takes the
// lowest free slot and is saved by name in config.json (assignAudioSlots);
// a device that goes away keeps its slot and shows present:false. Only
// the app's reset (resetAudioSlots, POST /api/audio/slots/reset) empties
// them, after which the devices connected at that moment fill them again.
// Names, not ids, because an endpoint id changes when a driver is
// reinstalled or a USB device moves to another port.
//
// What is active comes from the newest device list: the tray heartbeat
// (every 30s) and the reply of every audio run that carries one (`audio
// get` does, so the background reading of session_fill.go keeps it
// current without a tray app). The default output is the audio sample's
// device, which the tray app checks every 3s: a switch in the Windows
// volume flyout reaches the hub as the usual audio.changed. A change of
// the lists alone (a device plugged in, the default input switched)
// pushes audio.changed too.

import (
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/status"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// audioDeviceList is the newest list of one direction and when it was read.
type audioDeviceList struct {
	list []useraction.AudioEndpoint
	at   time.Time
}

// audioDevs holds the newest lists. Like the audio sample, an older
// reading never overwrites a newer one.
var audioDevs struct {
	mu      sync.Mutex
	outputs audioDeviceList
	inputs  audioDeviceList
}

// note stores list taken at at unless the stored one is newer. changed is
// true when the names or the default differ from a previous reading (the
// first one is a baseline).
func (d *audioDeviceList) note(list []useraction.AudioEndpoint, at time.Time) (changed bool) {
	if !d.at.IsZero() && at.Before(d.at) {
		return false
	}
	changed = !d.at.IsZero() && !sameEndpoints(d.list, list)
	d.list, d.at = slices.Clone(list), at
	return changed
}

// sameEndpoints compares two lists by what status shows: the names and
// the default, in order.
func sameEndpoints(a, b []useraction.AudioEndpoint) bool {
	return slices.Equal(a, b)
}

// noteAudioDevices stores the lists that are given (hasOut, hasIn), gives
// the devices without a slot one (assignAudioSlots) and reports whether
// either the lists or the slots changed.
func noteAudioDevices(outputs, inputs []useraction.AudioEndpoint, hasOut, hasIn bool, at time.Time) (changed bool) {
	audioDevs.mu.Lock()
	if hasOut && audioDevs.outputs.note(outputs, at) {
		changed = true
	}
	if hasIn && audioDevs.inputs.note(inputs, at) {
		changed = true
	}
	audioDevs.mu.Unlock()
	if assignAudioSlots() {
		changed = true
	}
	return changed
}

// currentAudioDevices returns the newest lists; ok is false for a
// direction with no reading since the service started (or the target
// session changed).
func currentAudioDevices() (outputs, inputs []useraction.AudioEndpoint, outOK, inOK bool) {
	audioDevs.mu.Lock()
	defer audioDevs.mu.Unlock()
	return slices.Clone(audioDevs.outputs.list), slices.Clone(audioDevs.inputs.list),
		!audioDevs.outputs.at.IsZero(), !audioDevs.inputs.at.IsZero()
}

// resetAudioDevices forgets both lists: the target session changed, or a
// test starts.
func resetAudioDevices() {
	audioDevs.mu.Lock()
	audioDevs.outputs, audioDevs.inputs = audioDeviceList{}, audioDeviceList{}
	audioDevs.mu.Unlock()
}

// ---- slot assignment (#159) -------------------------------------------------

// audioSlotsMu serialises the read-modify-save of the slots.
var audioSlotsMu sync.Mutex

// saveSlots stores a config whose slots changed: saveConfig, set in init
// because saveConfig reaches the user-action runner, whose replies reach
// back here (an initialization cycle otherwise).
var saveSlots func(Config) error

func init() { saveSlots = saveConfig }

// slotOrder is the names of a device list, the default first: on an empty
// list the device in use gets slot 1.
func slotOrder(list []useraction.AudioEndpoint) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		if e.Default {
			out = append(out, e.Name)
		}
	}
	for _, e := range list {
		if !e.Default {
			out = append(out, e.Name)
		}
	}
	return out
}

// withAssignedSlots is cfg with the devices of the stored lists put into
// free slots (config.AssignAudioSlots); changed reports whether any were.
func withAssignedSlots(cfg Config) (Config, bool) {
	outputs, inputs, _, _ := currentAudioDevices()
	var outChanged, inChanged bool
	cfg.Audio.Outputs, outChanged = config.AssignAudioSlots(cfg.Audio.Outputs, slotOrder(outputs))
	cfg.Audio.Inputs, inChanged = config.AssignAudioSlots(cfg.Audio.Inputs, slotOrder(inputs))
	return cfg, outChanged || inChanged
}

// assignAudioSlots gives every listed device without a slot the lowest
// free one and saves config.json when that changed anything. Only while
// media.enabled is on: the slots are part of the audio block, which that
// switch gates (and no device lists arrive without it anyway).
func assignAudioSlots() bool {
	audioSlotsMu.Lock()
	defer audioSlotsMu.Unlock()
	cfg := getConfig()
	if !cfg.Media.Enabled {
		return false
	}
	cfg, changed := withAssignedSlots(cfg)
	if !changed {
		return false
	}
	if err := saveSlots(cfg); err != nil {
		logMsg("audio: the new device slots could not be saved: %v", err)
		return false
	}
	logMsg("audio: device slots now outputs=%q inputs=%q", cfg.Audio.Outputs, cfg.Audio.Inputs)
	return true
}

// resetAudioSlots empties both slot lists and fills them again from the
// devices connected now (the app's 초기화 button). It returns the saved
// slots and pushes audio.changed.
func resetAudioSlots() (config.AudioConfig, error) {
	audioSlotsMu.Lock()
	cfg := getConfig()
	cfg.Audio = config.AudioConfig{Outputs: []string{}, Inputs: []string{}}
	if cfg.Media.Enabled {
		cfg, _ = withAssignedSlots(cfg)
	}
	err := saveSlots(cfg)
	audioSlotsMu.Unlock()
	if err != nil {
		return config.AudioConfig{}, err
	}
	saved := getConfig().Audio
	logMsg("audio: device slots reset to outputs=%q inputs=%q", saved.Outputs, saved.Inputs)
	emitAudioDevicesChanged()
	return saved, nil
}

// ---- readings ---------------------------------------------------------------

// emitAudioDevicesChanged pushes audio.changed for a change of the lists
// or the slots: the status it carries has the new slots. The data is the
// audio sample's, as for a volume change.
func emitAudioDevicesChanged() {
	if s, ok := currentAudio(); ok {
		emitAudioChanged(s.Audio)
		return
	}
	emitDevice("audio", "changed", map[string]string{})
}

// endpointsFromReply reads the "outputs" or "inputs" list of a reply.
// ok is false when the reply has none or one that fails validation.
func endpointsFromReply(res UserActionResult, key string) ([]useraction.AudioEndpoint, bool) {
	raw, ok := res.Fields[key]
	if !ok {
		return nil, false
	}
	var list []useraction.AudioEndpoint
	if err := json.Unmarshal(raw, &list); err != nil {
		logMsg("user-action reply: ignoring %s: %v", key, err)
		return nil, false
	}
	if err := useraction.ValidateEndpoints(list); err != nil {
		logMsg("user-action reply: ignoring %s: %v", key, err)
		return nil, false
	}
	if list == nil {
		list = []useraction.AudioEndpoint{}
	}
	return list, true
}

// noteReplyAudioDevices stores the device lists of an ok reply. It returns
// what to call once the reply's audio block is stored too: that pushes
// audio.changed for a change of the lists, unless storing the audio block
// pushed one already.
func noteReplyAudioDevices(res UserActionResult) (settle func()) {
	outputs, hasOut := endpointsFromReply(res, "outputs")
	inputs, hasIn := endpointsFromReply(res, "inputs")
	if !hasOut && !hasIn {
		return func() {}
	}
	before, hadBefore := currentAudio()
	changed := noteAudioDevices(outputs, inputs, hasOut, hasIn, clock.audio())
	return func() {
		if !changed {
			return
		}
		if after, ok := currentAudio(); ok && hadBefore && after.Audio != before.Audio {
			return // recordAudioSample pushed audio.changed
		}
		emitAudioDevicesChanged()
	}
}

// AudioDevices stores the tray app's lists (#145). Called only for a
// heartbeat from the target session, like Audio.
func (h *heartbeatStore) AudioDevices(outputs, inputs []useraction.AudioEndpoint, at time.Time) {
	noteTrayReport()
	if noteAudioDevices(outputs, inputs, true, true, at) {
		emitAudioDevicesChanged()
	}
}

// ---- status ----------------------------------------------------------------

// stAudioStatus is the audio block for cfg: the reading (media.go) and the
// device slots.
func stAudioStatus(cfg Config) stAudio {
	a := stAudioReading(cfg)
	return withAudioSlots(cfg, a, func() bool { return a.Available || sys.sessionPresent() })
}

// withAudioSlots adds the slot lists to an audio block. Nothing is present
// or default unless someone is logged in (session, asked only when it
// matters) and a device list has been read; the default output is the
// block's own device.
func withAudioSlots(cfg Config, a stAudio, session func() bool) stAudio {
	a.Outputs, a.Inputs = []status.AudioSlot{}, []status.AudioSlot{}
	if !cfg.Media.Enabled {
		return a
	}
	outputs, inputs, outOK, inOK := currentAudioDevices()
	defaultOut := ""
	if a.Available && a.Device != nil {
		defaultOut = *a.Device
	}
	a.Outputs = audioSlotList(cfg.Audio.Outputs, outputs, a.Available, func(name string) bool {
		return defaultOut != "" && config.SameAudioDevice(name, defaultOut)
	})
	a.Outputs = presentOnlyIf(a.Outputs, outOK, defaultOut)
	live := inOK && config.HasAudioSlots(cfg.Audio.Inputs) && session()
	a.Inputs = audioSlotList(cfg.Audio.Inputs, inputs, live, func(name string) bool {
		return slices.ContainsFunc(inputs, func(e useraction.AudioEndpoint) bool { return e.Default && config.SameAudioDevice(e.Name, name) })
	})
	return a
}

// audioSlotList builds the slots of names against devices. Without live
// every slot is listed as neither present nor default.
func audioSlotList(names []string, devices []useraction.AudioEndpoint, live bool, isDefault func(string) bool) []status.AudioSlot {
	out := []status.AudioSlot{}
	for i, name := range names {
		if name == "" || i >= config.AudioSlots {
			continue
		}
		s := status.AudioSlot{Slot: i + 1, Name: name}
		if live {
			s.Default = isDefault(name)
			s.Present = s.Default || slices.ContainsFunc(devices, func(e useraction.AudioEndpoint) bool { return config.SameAudioDevice(e.Name, name) })
		}
		out = append(out, s)
	}
	return out
}

// presentOnlyIf keeps an output slot present without a device list only
// when it is the default device (the audio sample saw it).
func presentOnlyIf(slots []status.AudioSlot, listed bool, defaultOut string) []status.AudioSlot {
	if listed {
		return slots
	}
	for i := range slots {
		slots[i].Present = defaultOut != "" && config.SameAudioDevice(slots[i].Name, defaultOut)
	}
	return slots
}
