package useraction

// A minimal Core Audio binding (#104): just enough of IMMDeviceEnumerator,
// IMMDevice, IPropertyStore and IAudioEndpointVolume to read and set the
// default playback device's master volume and mute, and to read its
// friendly name. The vtables are called directly with syscall.SyscallN; no
// COM library is involved, and neither is PowerShell — its Add-Type start
// alone costs about a second, far too slow for a volume slider (§2).
//
// Every call here must happen on one OS thread that has COM initialised;
// withCOM arranges that. Interface pointers never leave that thread.

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
	procPropVariantClear = modOle32.NewProc("PropVariantClear")
)

var (
	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioEndpointVolume = windows.GUID{Data1: 0x5CDF2C82, Data2: 0x841E, Data3: 0x4546, Data4: [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A}}
	// pkeyDeviceFriendlyName is PKEY_Device_FriendlyName: "스피커(Realtek(R) Audio)".
	pkeyDeviceFriendlyName = propertyKey{
		fmtid: windows.GUID{Data1: 0xA45C254E, Data2: 0xDF1C, Data3: 0x4EFD, Data4: [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0}},
		pid:   14,
	}
)

const (
	clsctxAll   = 0x17 // CLSCTX_INPROC_SERVER|INPROC_HANDLER|LOCAL_SERVER|REMOTE_SERVER
	eRender     = 0
	eConsole    = 0
	stgmRead    = 0
	vtLPWSTR    = 31
	sOK         = 0
	sFalse      = 1
	rpcEChanged = 0x80010106 // RPC_E_CHANGED_MODE
	eNotFound   = 0x80070490 // HRESULT_FROM_WIN32(ERROR_NOT_FOUND): no such endpoint
	coinitMTA   = 0x0        // COINIT_MULTITHREADED
)

// Vtable slots (IUnknown's QueryInterface/AddRef/Release are 0..2).
const (
	slotRelease = 2

	// IMMDeviceEnumerator
	slotGetDefaultAudioEndpoint = 4
	// IMMDevice
	slotActivate          = 3
	slotOpenPropertyStore = 4
	// IPropertyStore
	slotGetValue = 5
	// IAudioEndpointVolume
	slotSetMasterVolumeLevelScalar = 7
	slotGetMasterVolumeLevelScalar = 9
	slotSetMute                    = 14
	slotGetMute                    = 15
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is PROPVARIANT: a 2-byte type tag, 6 reserved bytes and a
// union the size of two pointers (16 bytes on 64-bit, 8+ on 32-bit). Only
// VT_LPWSTR is ever read, whose value is the first pointer.
type propVariant struct {
	vt  uint16
	_   [3]uint16
	val *uint16 // pwszVal
	_   uintptr
}

// comObject is an interface pointer: a pointer to a pointer to its vtable.
type comObject struct {
	vtbl *[32]uintptr
}

// call invokes vtable slot i with the object as the first argument and
// returns the HRESULT.
func (o *comObject) call(slot int, args ...uintptr) uint32 {
	fn := o.vtbl[slot]
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return uint32(r)
}

func (o *comObject) release() {
	if o != nil {
		o.call(slotRelease)
	}
}

// hresultError describes a failed COM call.
type hresultError struct {
	op string
	hr uint32
}

func (e *hresultError) Error() string {
	return fmt.Sprintf("%s: HRESULT 0x%08X (%v)", e.op, e.hr, syscall.Errno(e.hr&0xFFFF))
}

func check(op string, hr uint32) error {
	if hr == sOK || hr == sFalse {
		return nil
	}
	return &hresultError{op: op, hr: hr}
}

// errNoPlaybackDevice is returned when Windows has no default render
// endpoint (no speakers, all disabled, audio service stopped).
var errNoPlaybackDevice = errors.New("no default playback device")

// withCOM runs f on a locked OS thread with COM initialised (MTA). A thread
// Go reuses may already be in an apartment; RPC_E_CHANGED_MODE means COM is
// usable there all the same, but that init is not ours to undo.
func withCOM(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr := uint32(0)
	if err := windows.CoInitializeEx(0, coinitMTA); err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			hr = uint32(errno)
		}
		if hr != rpcEChanged && hr != sFalse {
			return fmt.Errorf("CoInitializeEx: %w", err)
		}
	}
	if hr != rpcEChanged {
		defer windows.CoUninitialize()
	}
	return f()
}

// coreAudioEndpoint is the default playback device's volume control. It
// implements audioEndpoint; open it and use it inside one withCOM call.
type coreAudioEndpoint struct {
	volume *comObject // IAudioEndpointVolume
	device string
}

// openDefaultEndpoint activates IAudioEndpointVolume on the default
// eRender/eConsole endpoint. The friendly name is best effort: a device
// whose property store cannot be read still has a volume.
func openDefaultEndpoint() (audioEndpoint, error) {
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
	defer enum.release()

	var dev *comObject
	if hr := enum.call(slotGetDefaultAudioEndpoint, eRender, eConsole, uintptr(unsafe.Pointer(&dev))); hr != sOK {
		if hr == eNotFound {
			return nil, errNoPlaybackDevice
		}
		return nil, check("GetDefaultAudioEndpoint", hr)
	}
	defer dev.release()

	var vol *comObject
	if err := check("IMMDevice.Activate(IAudioEndpointVolume)",
		dev.call(slotActivate, uintptr(unsafe.Pointer(&iidIAudioEndpointVolume)), clsctxAll, 0, uintptr(unsafe.Pointer(&vol)))); err != nil {
		return nil, err
	}
	return &coreAudioEndpoint{volume: vol, device: friendlyName(dev)}, nil
}

// friendlyName reads PKEY_Device_FriendlyName, or "" when it cannot.
func friendlyName(dev *comObject) string {
	var store *comObject
	if dev.call(slotOpenPropertyStore, stgmRead, uintptr(unsafe.Pointer(&store))) != sOK || store == nil {
		return ""
	}
	defer store.release()
	var pv propVariant
	if store.call(slotGetValue, uintptr(unsafe.Pointer(&pkeyDeviceFriendlyName)), uintptr(unsafe.Pointer(&pv))) != sOK {
		return ""
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	if pv.vt != vtLPWSTR || pv.val == nil {
		return ""
	}
	// The PROPVARIANT owns the string until PropVariantClear; copy it out.
	return sanitizeDevice(windows.UTF16PtrToString(pv.val))
}

func (e *coreAudioEndpoint) Volume() (float32, error) {
	var level float32
	if err := check("GetMasterVolumeLevelScalar", e.volume.call(slotGetMasterVolumeLevelScalar, uintptr(unsafe.Pointer(&level)))); err != nil {
		return 0, err
	}
	return level, nil
}

// SetVolume passes the float in an integer register slot. The Windows x64
// convention puts a float argument in the XMM register of its position,
// and Go's syscall trampoline copies the first four integer arguments into
// XMM0..XMM3 for exactly this reason; on 386 it is a 4-byte stack slot
// either way.
func (e *coreAudioEndpoint) SetVolume(level float32) error {
	return check("SetMasterVolumeLevelScalar", e.volume.call(slotSetMasterVolumeLevelScalar, uintptr(math.Float32bits(level)), 0))
}

func (e *coreAudioEndpoint) Muted() (bool, error) {
	var b int32 // BOOL
	if err := check("GetMute", e.volume.call(slotGetMute, uintptr(unsafe.Pointer(&b)))); err != nil {
		return false, err
	}
	return b != 0, nil
}

func (e *coreAudioEndpoint) SetMuted(on bool) error {
	var b uintptr
	if on {
		b = 1
	}
	return check("SetMute", e.volume.call(slotSetMute, b, 0))
}

func (e *coreAudioEndpoint) Device() string { return e.device }

func (e *coreAudioEndpoint) Close() {
	e.volume.release()
	e.volume = nil
}
