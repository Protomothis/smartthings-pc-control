package useraction

// The active playback and recording devices (#145, #159): their names and
// which one is the default, for the device slots the service reports.
// Nothing here changes a device — switching the default one was dropped
// from v1.3.0 (#159), and with it the undocumented IPolicyConfig.
//
// `audio get` carries both lists next to the volume (best effort), so the
// service's background reading keeps them current without a tray app;
// the tray app reads them in-process for its 30s heartbeat.
//
//	{"ok":true,"audio":{…},"outputs":[{"name":…,"default":true},…],"inputs":[…]}

import (
	"errors"
	"fmt"
)

// MaxAudioEndpoints bounds one list; a PC with more active endpoints of one
// direction than this reports the first ones.
const MaxAudioEndpoints = 32

// maxEndpointIDRunes bounds an endpoint id read from Windows (real ones are
// about 55 characters). Ids never leave this process: they only tell the
// default device apart from the others.
const maxEndpointIDRunes = 256

// AudioEndpoint is one active playback or recording device.
type AudioEndpoint struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// Validate applies Audio.Validate's name rules.
func (e AudioEndpoint) Validate() error {
	if err := (Audio{Device: e.Name}).Validate(); err != nil {
		return err
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
	flowCapture audioFlow = eCapture
)

// audioDevices is the part of Core Audio the lists use, behind an
// interface so they can be tested without sound cards.
type audioDevices interface {
	// List returns the active endpoints of flow, the default marked.
	List(flow audioFlow) ([]AudioEndpoint, error)
	Close()
}

// openAudioDevices opens the device enumerator; tests replace it.
var openAudioDevices = openCoreAudioDevices

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
// tray app calls it in-process for its heartbeat and the features tab's
// slot list. The error is a *Error (failed).
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
		var ue *Error
		if errors.As(err, &ue) {
			return nil, nil, err
		}
		return nil, nil, Failed("audio devices: %v", err)
	}
	return outputs, inputs, nil
}
