package useraction

// The Core Audio half of the device verbs (#145), in the style of
// coreaudio.go: vtables called directly, on the withCOM thread, every
// out-pointer converted inside the o.call(...) argument list (#121).
//
// Listing is documented API: IMMDeviceEnumerator::EnumAudioEndpoints over
// the active endpoints, each one's GetId and PKEY_Device_FriendlyName.
//
// Switching is not: Windows has no public call that sets the default
// endpoint. Every tool that does (the Sound control panel itself,
// EndPointController, SoundSwitch, AudioDeviceCmdlets) goes through the
// undocumented IPolicyConfig of CLSID_PolicyConfigClient (AudioSes.dll,
// ThreadingModel Both), whose IID f8679f50-… has been the same from
// Windows 7 through 11. SetDefaultEndpoint is called once per role —
// eConsole, eMultimedia and eCommunications — which is what the control
// panel's "Set as default" plus "Set as default communication device" do.
// Should the class ever stop answering that IID, the Vista-era
// IPolicyConfigVista (its own class, SetDefaultEndpoint one slot earlier)
// is tried before giving up.

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	clsidPolicyConfigClient       = windows.GUID{Data1: 0x870AF99C, Data2: 0x171D, Data3: 0x4F9E, Data4: [8]byte{0xAF, 0x0D, 0xE6, 0x3D, 0xF4, 0x0C, 0x2B, 0xC9}}
	iidIPolicyConfig              = windows.GUID{Data1: 0xF8679F50, Data2: 0x850A, Data3: 0x41CF, Data4: [8]byte{0x9C, 0x72, 0x43, 0x0F, 0x29, 0x02, 0x90, 0xC8}}
	clsidCPolicyConfigVistaClient = windows.GUID{Data1: 0x294935CE, Data2: 0xF637, Data3: 0x4E7C, Data4: [8]byte{0xA4, 0x1B, 0xAB, 0x25, 0x54, 0x60, 0xB8, 0x62}}
	iidIPolicyConfigVista         = windows.GUID{Data1: 0x568B9108, Data2: 0x44BF, Data3: 0x40B4, Data4: [8]byte{0x90, 0x06, 0x86, 0xAF, 0xE5, 0xB5, 0xA6, 0x20}}
)

const (
	deviceStateActive = 0x1 // DEVICE_STATE_ACTIVE
	eMultimedia       = 1
	eCommunications   = 2
)

// Vtable slots beyond coreaudio.go's.
const (
	// IMMDeviceEnumerator
	slotEnumAudioEndpoints = 3
	// IMMDeviceCollection
	slotCollectionGetCount = 3
	slotCollectionItem     = 4
	// IMMDevice
	slotGetID = 5
	// IPolicyConfig: GetMixFormat … SetPropertyValue (3–12), then
	// SetDefaultEndpoint. IPolicyConfigVista lacks ResetDeviceFormat, so
	// its SetDefaultEndpoint is one slot earlier.
	slotPolicySetDefaultEndpoint      = 13
	slotPolicyVistaSetDefaultEndpoint = 12
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
// be read is skipped (there is nothing to switch to); one without a
// friendly name is listed under "".
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
		out = append(out, AudioEndpoint{ID: id, Name: name, Default: defaultID != "" && id == defaultID})
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
	if len(id) > MaxEndpointIDRunes || hasControl(id) {
		return ""
	}
	return id
}

// SetDefault makes id the default endpoint for all three roles.
func (d *coreAudioDevices) SetDefault(id string) error {
	wid, err := windows.UTF16PtrFromString(id)
	if err != nil {
		return err
	}
	policy, slot, err := newPolicyConfig()
	if err != nil {
		return err
	}
	defer policy.release()
	for _, role := range []uintptr{eConsole, eMultimedia, eCommunications} {
		if err := check("IPolicyConfig.SetDefaultEndpoint", policy.call(slot, uintptr(unsafe.Pointer(wid)), role)); err != nil {
			return err
		}
	}
	return nil
}

// newPolicyConfig creates the policy object and returns the slot of its
// SetDefaultEndpoint.
func newPolicyConfig() (*comObject, int, error) {
	try := func(clsid, iid *windows.GUID) (*comObject, error) {
		var obj *comObject
		hr, _, _ := procCoCreateInstance.Call(
			uintptr(unsafe.Pointer(clsid)),
			0,
			clsctxAll,
			uintptr(unsafe.Pointer(iid)),
			uintptr(unsafe.Pointer(&obj)),
		)
		if err := check("CoCreateInstance(PolicyConfig)", uint32(hr)); err != nil {
			return nil, err
		}
		return obj, nil
	}
	obj, err := try(&clsidPolicyConfigClient, &iidIPolicyConfig)
	if err == nil {
		return obj, slotPolicySetDefaultEndpoint, nil
	}
	if vista, verr := try(&clsidCPolicyConfigVistaClient, &iidIPolicyConfigVista); verr == nil {
		return vista, slotPolicyVistaSetDefaultEndpoint, nil
	}
	return nil, 0, err
}
