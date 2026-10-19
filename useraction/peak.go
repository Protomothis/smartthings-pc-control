package useraction

// The playback peak meter (#159): is sound coming out of the default
// playback device right now? IAudioMeterInformation::GetPeakValue on the
// default eRender/eConsole endpoint, activated like the volume control in
// coreaudio.go, with the out-pointer converted inside the o.call(...)
// argument list (#121).
//
// GetPeakValue is the highest sample of the endpoint's stream over the
// last audio-engine period (about 10 ms), as the volume flyout's level bar
// shows it: 0.0 for silence, up to 1.0. Whether an endpoint mute also
// zeroes it depends on where the device applies its volume (hardware or
// the engine), so "playing" means "a stream is sounding", and mute is
// reported next to it for a routine to combine. One reading says little;
// the tray app samples it a few times a second and turns that into
// "playing" (gui/playing.go).
//
// ReadPeak is for in-process use by the tray app only: a peak is not a
// state a one-shot `user-action` run could report usefully.

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var iidIAudioMeterInformation = windows.GUID{Data1: 0xC02216F6, Data2: 0x8C67, Data3: 0x4B5B, Data4: [8]byte{0x9D, 0x00, 0xD0, 0x08, 0xE7, 0x3E, 0x00, 0x64}}

// IAudioMeterInformation: GetPeakValue right after IUnknown.
const slotGetPeakValue = 3

// readPeak reads the meter once inside a COM scope; tests replace it.
var readPeak = readCorePeak

// ReadPeak returns the default playback device's peak level, 0.0..1.0.
// The error is a *Error: unsupported when there is no playback device,
// failed when a Core Audio call did not work.
func ReadPeak() (float32, error) {
	var peak float32
	err := runCOM(func() error {
		var err error
		peak, err = readPeak()
		return err
	})
	switch {
	case err == nil:
		return min(max(peak, 0), 1), nil
	case errors.Is(err, errNoPlaybackDevice):
		return 0, Unsupported("%v", err)
	}
	return 0, Failed("peak meter: %v", err)
}

// readCorePeak activates IAudioMeterInformation on the default playback
// endpoint and reads GetPeakValue. It opens the endpoint afresh every
// time, so a change of the default device is followed at once.
func readCorePeak() (float32, error) {
	dev, err := defaultDevice(flowRender)
	if err != nil {
		return 0, err
	}
	defer dev.release()

	var meter *comObject
	if err := check("IMMDevice.Activate(IAudioMeterInformation)",
		dev.call(slotActivate, uintptr(unsafe.Pointer(&iidIAudioMeterInformation)), clsctxAll, 0, uintptr(unsafe.Pointer(&meter)))); err != nil {
		return 0, err
	}
	defer meter.release()

	var peak float32
	if err := check("IAudioMeterInformation.GetPeakValue", meter.call(slotGetPeakValue, uintptr(unsafe.Pointer(&peak)))); err != nil {
		return 0, err
	}
	return peak, nil
}
