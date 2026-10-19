package useraction

// The Core Audio half of the device lists (#145), in the style of
// coreaudio.go: vtables called directly, on the withCOM thread, every
// out-pointer converted inside the o.call(...) argument list (#121).
//
// All documented API: IMMDeviceEnumerator::EnumAudioEndpoints over the
// active endpoints, each one's GetId (to mark the default) and
// PKEY_Device_FriendlyName.

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const deviceStateActive = 0x1 // DEVICE_STATE_ACTIVE

// Vtable slots beyond coreaudio.go's.
const (
	// IMMDeviceEnumerator
	slotEnumAudioEndpoints = 3
	// IMMDeviceCollection
	slotCollectionGetCount = 3
	slotCollectionItem     = 4
	// IMMDevice
	slotGetID = 5
)

// coreAudioDevices implements audioDevices; open and use it inside one
// withCOM call.
type coreAudioDevices struct {
	enum *comObject // IMMDeviceEnumerator
}

func openCoreAudioDevices() (audioDevices, error) {
	enum, err := newDeviceEnumerator()
	if err != nil {
		return nil, err
	}
	return &coreAudioDevices{enum: enum}, nil
}

// newDeviceEnumerator creates an IMMDeviceEnumerator.
func newDeviceEnumerator() (*comObject, error) {
	var enum *comObject
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)),
		0,
		clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enum)),
	)
	if err := check("CoCreateInstance(MMDeviceEnumerator)", uint32(hr)); err != nil {
		return nil, err
	}
	return enum, nil
}

func (d *coreAudioDevices) Close() {
	d.enum.release()
	d.enum = nil
}

// List enumerates the active endpoints of flow. A device whose id cannot
// be read is skipped (it could not be told apart from the default); one
// without a friendly name is listed under "".
func (d *coreAudioDevices) List(flow audioFlow) ([]AudioEndpoint, error) {
	defaultID := d.defaultID(flow)

	var coll *comObject
	if err := check("EnumAudioEndpoints",
		d.enum.call(slotEnumAudioEndpoints, uintptr(flow), deviceStateActive, uintptr(unsafe.Pointer(&coll)))); err != nil {
		return nil, err
	}
	defer coll.release()
	var n uint32
	if err := check("IMMDeviceCollection.GetCount", coll.call(slotCollectionGetCount, uintptr(unsafe.Pointer(&n)))); err != nil {
		return nil, err
	}
	out := []AudioEndpoint{}
	for i := uint32(0); i < n && len(out) < MaxAudioEndpoints; i++ {
		var dev *comObject
		if coll.call(slotCollectionItem, uintptr(i), uintptr(unsafe.Pointer(&dev))) != sOK || dev == nil {
			continue
		}
		id := deviceID(dev)
		name := friendlyName(dev)
		dev.release()
		if id == "" {
			continue
		}
		out = append(out, AudioEndpoint{Name: name, Default: defaultID != "" && id == defaultID})
	}
	return out, nil
}

// defaultID is the id of flow's default console endpoint, "" when there
// is none.
func (d *coreAudioDevices) defaultID(flow audioFlow) string {
	var dev *comObject
	if d.enum.call(slotGetDefaultAudioEndpoint, uintptr(flow), eConsole, uintptr(unsafe.Pointer(&dev))) != sOK || dev == nil {
		return ""
	}
	defer dev.release()
	return deviceID(dev)
}

// deviceID reads IMMDevice::GetId, "" when it cannot. The string is the
// callee's CoTaskMemAlloc; it is copied and freed.
func deviceID(dev *comObject) string {
	var p *uint16
	if dev.call(slotGetID, uintptr(unsafe.Pointer(&p))) != sOK || p == nil {
		return ""
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(p))
	id := windows.UTF16PtrToString(p)
	if len(id) > maxEndpointIDRunes || hasControl(id) {
		return ""
	}
	return id
}
