package service

// Output and input device switching (#145).
//
// The user fills up to five output and five input slots with device names
// (config audio.outputs / audio.inputs). SmartThings, the app and
// Telegram switch by slot number: audio_output / audio_input with value =
// slot. The service finds the slot's device among the active ones (`audio
// outputs|inputs` in the user session) by name and makes it the default
// (`audio set-output|set-input --id`). Names, not ids, because an
// endpoint id changes when a driver is reinstalled or a USB device moves
// to another port.
//
// The status audio block lists the filled slots with present and default.
// What is active comes from the newest device list: the tray heartbeat
// (every 30s), the reply of every audio run that carries one (`audio
// get` does, so the background reading of session_fill.go keeps it
// current without a tray app), and the switches themselves. The default
// output is the audio sample's device, which the tray app checks every
// 3s: a switch in the Windows volume flyout reaches the hub as the usual
// audio.changed. A change of the lists alone (a device plugged in, the
// default input switched) pushes audio.changed too.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/action"
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
	return slices.EqualFunc(a, b, func(x, y useraction.AudioEndpoint) bool {
		return x.Name == y.Name && x.Default == y.Default
	})
}

// noteAudioDevices stores the lists that are given (hasOut, hasIn) and
// reports whether either changed.
func noteAudioDevices(outputs, inputs []useraction.AudioEndpoint, hasOut, hasIn bool, at time.Time) (changed bool) {
	audioDevs.mu.Lock()
	defer audioDevs.mu.Unlock()
	if hasOut && audioDevs.outputs.note(outputs, at) {
		changed = true
	}
	if hasIn && audioDevs.inputs.note(inputs, at) {
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

// emitAudioDevicesChanged pushes audio.changed for a change of the lists:
// the status it carries has the new slots. The data is the audio sample's,
// as for a volume change.
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
// pushed one already (a set-output reply changes both).
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
		return defaultOut != "" && sameDeviceName(name, defaultOut)
	})
	a.Outputs = presentOnlyIf(a.Outputs, outOK, defaultOut)
	live := inOK && config.HasAudioSlots(cfg.Audio.Inputs) && session()
	a.Inputs = audioSlotList(cfg.Audio.Inputs, inputs, live, func(name string) bool {
		return slices.ContainsFunc(inputs, func(e useraction.AudioEndpoint) bool { return e.Default && sameDeviceName(e.Name, name) })
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
			s.Present = s.Default || slices.ContainsFunc(devices, func(e useraction.AudioEndpoint) bool { return sameDeviceName(e.Name, name) })
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
		slots[i].Present = defaultOut != "" && sameDeviceName(slots[i].Name, defaultOut)
	}
	return slots
}

// sameDeviceName matches a slot's name with a device's friendly name:
// spaces around and letter case do not count.
func sameDeviceName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// ---- commands --------------------------------------------------------------

// runAudioDeviceCommand switches the default output (audio_output) or
// input (audio_input) to the device of slot *value. runMediaCommand has
// checked media.enabled. err is a *action.ValueError, an
// *action.AudioDeviceError (slot_empty, device_missing, switch_failed),
// or errNoUserSession / errUserActionTimeout as for the volume.
func runAudioDeviceCommand(ctx context.Context, name string, value *int) (UserActionResult, error) {
	slot, err := action.AudioSlotValue(value)
	if err != nil {
		return UserActionResult{}, err
	}
	cfg := getConfig()
	names, listVerb, setVerb := cfg.Audio.Outputs, "outputs", "set-output"
	if name == action.CommandAudioInput {
		names, listVerb, setVerb = cfg.Audio.Inputs, "inputs", "set-input"
	}
	want := config.AudioSlotName(names, slot)
	if want == "" {
		return UserActionResult{}, &action.AudioDeviceError{Code: action.AudioSlotEmpty,
			Message: fmt.Sprintf("slot %d has no device", slot)}
	}

	res, err := userRun.media(ctx, "audio", listVerb)
	if err != nil {
		return res, audioSwitchError(err)
	}
	// The runner stored the list already (storeUserActionReply); storing
	// it again changes nothing then and keeps the store right for a
	// runner that does not.
	noteReplyAudioDevices(res)()
	list, _ := endpointsFromReply(res, listVerb)
	id := ""
	for _, e := range list {
		if sameDeviceName(e.Name, want) {
			id = e.ID
			break
		}
	}
	if id == "" {
		return res, &action.AudioDeviceError{Code: action.AudioDeviceMissing,
			Message: fmt.Sprintf("%s is not connected", want)}
	}

	res, err = userRun.media(ctx, "audio", setVerb, "--id", id)
	if err != nil {
		return res, audioSwitchError(err)
	}
	settle := noteReplyAudioDevices(res)
	if res.Audio != nil && res.Audio.Validate() == nil {
		recordAudioSample(*res.Audio, clock.audio())
	}
	settle()
	logMsg("audio: default %s device set to slot %d", strings.TrimSuffix(listVerb, "s"), slot)
	return res, nil
}

// audioSwitchError maps a failed run of a switch: no session and timeouts
// keep their media answers, a device gone between the list and the switch
// is device_missing, anything else switch_failed.
func audioSwitchError(err error) error {
	if errors.Is(err, errNoUserSession) || errors.Is(err, errUserActionTimeout) {
		return err
	}
	var ue *userActionError
	if errors.As(err, &ue) {
		if ue.Reason == useraction.AudioDeviceMissing {
			return &action.AudioDeviceError{Code: action.AudioDeviceMissing, Message: ue.Message}
		}
		logMsg("audio: switch failed: %v", err)
		return &action.AudioDeviceError{Code: action.AudioSwitchFailed, Message: ue.Message}
	}
	logMsg("audio: switch failed: %v", err)
	return &action.AudioDeviceError{Code: action.AudioSwitchFailed, Message: "the switch could not be run in the user session"}
}
