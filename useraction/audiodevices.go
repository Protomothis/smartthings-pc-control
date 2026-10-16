package useraction

// Audio device switching (#145): list the active playback and recording
// endpoints and make one of them the default.
//
//	user-action audio outputs                 {"ok":true,"outputs":[{"id":…,"name":…,"default":true},…]}
//	user-action audio inputs                  {"ok":true,"inputs":[…]}
//	user-action audio set-output --id <id>    {"ok":true,"outputs":[…],"audio":{…}}
//	user-action audio set-input --id <id>     {"ok":true,"inputs":[…],"audio":{…}}
//
// A set reply carries the list read back after the switch, and the
// default playback device's audio block (best effort): after set-output
// that is the new device's volume, and either way it lets the service
// refresh its audio store and status in one go. `audio get` also carries
// both lists (best effort), so the service's background reading keeps
// them current without a tray app.
//
// The id is the endpoint id string IMMDevice::GetId returns
// ("{0.0.0.00000000}.{guid}"). A set refuses an id that is not an active
// endpoint of that direction (reason device_missing) rather than handing
// it to the undocumented policy interface.

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Reasons of a refused set-output/set-input (Error.Reason).
const (
	// AudioDeviceMissing: the id is not an active endpoint now (unplugged,
	// disabled, another direction).
	AudioDeviceMissing = "device_missing"
	// AudioSwitchFailed: Windows refused to make it the default.
	AudioSwitchFailed = "switch_failed"
)

// Limits of the device lists.
const (
	// MaxEndpointIDRunes bounds --id. Real ids are about 55 characters.
	MaxEndpointIDRunes = 256
	// MaxAudioEndpoints bounds one list; a PC with more active endpoints
	// of one direction than this reports the first ones.
	MaxAudioEndpoints = 32
)

// AudioEndpoint is one active playback or recording device. ID is left
// out where only names travel (the tray heartbeat).
type AudioEndpoint struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// Validate applies Audio.Validate's name rules and the id limit.
func (e AudioEndpoint) Validate() error {
	if err := (Audio{Device: e.Name}).Validate(); err != nil {
		return err
	}
	if utf8.RuneCountInString(e.ID) > MaxEndpointIDRunes || hasControl(e.ID) {
		return errors.New("endpoint id too long or contains control characters")
	}
	return nil
}

// ValidateEndpoints checks a whole list.
func ValidateEndpoints(list []AudioEndpoint) error {
	if len(list) > MaxAudioEndpoints {
		return fmt.Errorf("more than %d endpoints", MaxAudioEndpoints)
	}
	for _, e := range list {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// audioFlow is EDataFlow: eRender (playback) or eCapture (recording).
type audioFlow int

const (
	flowRender  audioFlow = eRender
	flowCapture audioFlow = 1 // eCapture
)

// deviceVerbs are the audio verbs of this file, with the flow they act on
// and whether they switch.
var deviceVerbs = map[string]struct {
	flow audioFlow
	set  bool
}{
	"outputs":    {flowRender, false},
	"inputs":     {flowCapture, false},
	"set-output": {flowRender, true},
	"set-input":  {flowCapture, true},
}

// listKey is the reply key of a flow's list.
func listKey(f audioFlow) string {
	if f == flowCapture {
		return "inputs"
	}
	return "outputs"
}

// isDeviceVerb reports whether verb is one of deviceVerbs.
func isDeviceVerb(verb string) bool {
	_, ok := deviceVerbs[verb]
	return ok
}

// parseAudioDevice parses the verbs of deviceVerbs; args[0] is the verb.
func parseAudioDevice(req Request, args []string) (Request, error) {
	v := deviceVerbs[req.Verb]
	if !v.set {
		if len(args) != 1 {
			return req, badArgs("audio %s takes no arguments", req.Verb)
		}
		return req, nil
	}
	flags, err := flagSet{valued: map[string]bool{"--id": true}}.parse(args[1:])
	if err != nil {
		return req, err
	}
	id, ok := flags["--id"]
	if !ok {
		return req, badArgs("usage: audio %s --id <endpoint id>", req.Verb)
	}
	req.DeviceID = id[0]
	if strings.TrimSpace(req.DeviceID) == "" {
		return req, badArgs("audio %s: --id is empty", req.Verb)
	}
	if utf8.RuneCountInString(req.DeviceID) > MaxEndpointIDRunes || hasControl(req.DeviceID) {
		return req, badArgs("audio %s: --id is too long or contains control characters", req.Verb)
	}
	return req, nil
}

// audioDevices is the part of Core Audio the device verbs use, behind an
// interface so they can be tested without sound cards — and without ever
// changing this machine's default device.
type audioDevices interface {
	// List returns the active endpoints of flow, the default marked.
	List(flow audioFlow) ([]AudioEndpoint, error)
	// SetDefault makes id the default endpoint of its flow for every role
	// (console, multimedia, communications).
	SetDefault(id string) error
	Close()
}

// openAudioDevices opens the device enumerator; tests replace it.
var openAudioDevices = openCoreAudioDevices

// audioDeviceHandler serves the verbs of deviceVerbs.
func audioDeviceHandler(req Request) (map[string]any, error) {
	v := deviceVerbs[req.Verb]
	out := map[string]any{}
	err := runCOM(func() error {
		devs, err := openAudioDevices()
		if err != nil {
			return err
		}
		defer devs.Close()
		if v.set {
			if err := switchDefault(devs, v.flow, req.DeviceID); err != nil {
				return err
			}
		}
		list, err := devs.List(v.flow)
		if err != nil {
			return err
		}
		out[listKey(v.flow)] = list
		return nil
	})
	if err != nil {
		var ue *Error
		if errors.As(err, &ue) {
			return nil, err
		}
		return nil, Failed("audio %s: %v", req.Verb, err)
	}
	if v.set {
		// The playback state after the switch; a PC left without a
		// playback device simply has no audio block.
		if a, err := ReadAudio(); err == nil {
			out["audio"] = a
		}
	}
	return out, nil
}

// switchDefault checks that id is an active endpoint of flow and makes it
// the default.
func switchDefault(devs audioDevices, flow audioFlow, id string) error {
	list, err := devs.List(flow)
	if err != nil {
		return err
	}
	found := false
	for _, e := range list {
		if strings.EqualFold(e.ID, id) {
			found = true
			break
		}
	}
	if !found {
		return &Error{Code: CodeFailed, Reason: AudioDeviceMissing,
			Message: fmt.Sprintf("no active %s device with that id", flowWord(flow))}
	}
	if err := devs.SetDefault(id); err != nil {
		return &Error{Code: CodeFailed, Reason: AudioSwitchFailed,
			Message: fmt.Sprintf("Windows did not switch the default %s device", flowWord(flow)),
			Detail:  err.Error()}
	}
	return nil
}

func flowWord(f audioFlow) string {
	if f == flowCapture {
		return "recording"
	}
	return "playback"
}

// addAudioDeviceLists puts both lists into an `audio get` reply, best
// effort: a failure leaves them out and the volume reply stands.
func addAudioDeviceLists(out map[string]any) {
	outputs, inputs, err := ReadAudioDevices()
	if err != nil {
		return
	}
	out["outputs"], out["inputs"] = outputs, inputs
}

// ReadAudioDevices lists the active playback and recording endpoints. The
// tray app calls it in-process for its heartbeat and the settings tab's
// device picker. The error is a *Error (failed).
func ReadAudioDevices() (outputs, inputs []AudioEndpoint, err error) {
	err = runCOM(func() error {
		devs, err := openAudioDevices()
		if err != nil {
			return err
		}
		defer devs.Close()
		if outputs, err = devs.List(flowRender); err != nil {
			return err
		}
		inputs, err = devs.List(flowCapture)
		return err
	})
	if err != nil {
		return nil, nil, Failed("audio devices: %v", err)
	}
	return outputs, inputs, nil
}
