package useraction

// A minimal DDC/CI binding (#144): user32's monitor enumeration, dxva2's
// physical-monitor and VCP calls, and QueryDisplayConfig for the names
// Windows shows in its display settings ("DELL U2720Q" rather than the
// "Generic PnP Monitor" dxva2 reports). Plain DLL calls, no COM; every
// pointer argument is converted inside the Proc.Call expression, which
// keeps it alive and in place (Proc.Call is //go:uintptrescapes).

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modDxva2 = windows.NewLazySystemDLL("dxva2.dll")

	procGetNumberOfPhysicalMonitorsFromHMONITOR = modDxva2.NewProc("GetNumberOfPhysicalMonitorsFromHMONITOR")
	procGetPhysicalMonitorsFromHMONITOR         = modDxva2.NewProc("GetPhysicalMonitorsFromHMONITOR")
	procDestroyPhysicalMonitors                 = modDxva2.NewProc("DestroyPhysicalMonitors")
	procGetVCPFeatureAndVCPFeatureReply         = modDxva2.NewProc("GetVCPFeatureAndVCPFeatureReply")
	procSetVCPFeature                           = modDxva2.NewProc("SetVCPFeature")
	procGetCapabilitiesStringLength             = modDxva2.NewProc("GetCapabilitiesStringLength")
	procCapabilitiesRequestAndCapabilitiesReply = modDxva2.NewProc("CapabilitiesRequestAndCapabilitiesReply")

	procEnumDisplayMonitors         = modUser32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW             = modUser32.NewProc("GetMonitorInfoW")
	procGetDisplayConfigBufferSizes = modUser32.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig          = modUser32.NewProc("QueryDisplayConfig")
	procDisplayConfigGetDeviceInfo  = modUser32.NewProc("DisplayConfigGetDeviceInfo")
)

const (
	monitorInfoPrimary = 0x1 // MONITORINFOF_PRIMARY

	qdcOnlyActivePaths = 0x2 // QDC_ONLY_ACTIVE_PATHS

	displayConfigGetSourceName = 1 // DISPLAYCONFIG_DEVICE_INFO_GET_SOURCE_NAME
	displayConfigGetTargetName = 2 // DISPLAYCONFIG_DEVICE_INFO_GET_TARGET_NAME

	// maxCapabilities bounds the capability string; real ones are a few
	// hundred bytes.
	maxCapabilities = 64 << 10
)

type rect struct{ left, top, right, bottom int32 }

// monitorInfoEx is MONITORINFOEXW.
type monitorInfoEx struct {
	cbSize  uint32
	monitor rect
	work    rect
	flags   uint32
	device  [32]uint16 // "\\.\DISPLAY1"
}

// physicalMonitor is PHYSICAL_MONITOR. The header packs it to 1 byte, which
// changes nothing here: a handle then 128 WCHARs has no padding either way.
type physicalMonitor struct {
	handle      windows.Handle
	description [128]uint16
}

// luid is LUID.
type luid struct {
	low  uint32
	high int32
}

// pathInfo is DISPLAYCONFIG_PATH_INFO (72 bytes): only the source and
// target ids are read.
type pathInfo struct {
	sourceAdapter luid
	sourceID      uint32
	sourceMode    uint32
	sourceStatus  uint32
	targetAdapter luid
	targetID      uint32
	targetRest    [9]uint32 // modeInfoIdx … statusFlags
	flags         uint32
}

// modeInfo is DISPLAYCONFIG_MODE_INFO (64 bytes), never read.
type modeInfo [64]byte

// deviceInfoHeader is DISPLAYCONFIG_DEVICE_INFO_HEADER.
type deviceInfoHeader struct {
	typ     uint32
	size    uint32
	adapter luid
	id      uint32
}

// sourceDeviceName is DISPLAYCONFIG_SOURCE_DEVICE_NAME.
type sourceDeviceName struct {
	header deviceInfoHeader
	gdi    [32]uint16
}

// targetDeviceName is DISPLAYCONFIG_TARGET_DEVICE_NAME.
type targetDeviceName struct {
	header            deviceInfoHeader
	flags             uint32
	outputTechnology  uint32
	edidManufactureID uint16
	edidProductCodeID uint16
	connectorInstance uint32
	friendlyName      [64]uint16
	devicePath        [128]uint16
}

// dxvaMonitor is one physical monitor's handle.
type dxvaMonitor windows.Handle

func (h dxvaMonitor) GetVCP(code byte) (uint32, uint32, error) {
	var kind, cur, maxv uint32
	r, _, err := procGetVCPFeatureAndVCPFeatureReply.Call(uintptr(h), uintptr(code),
		uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(&cur)), uintptr(unsafe.Pointer(&maxv)))
	if r == 0 {
		return 0, 0, fmt.Errorf("GetVCPFeature(0x%02X): %w", code, err)
	}
	return cur, maxv, nil
}

func (h dxvaMonitor) SetVCP(code byte, value uint32) error {
	r, _, err := procSetVCPFeature.Call(uintptr(h), uintptr(code), uintptr(value))
	if r == 0 {
		return fmt.Errorf("SetVCPFeature(0x%02X): %w", code, err)
	}
	return nil
}

func (h dxvaMonitor) Capabilities() (string, error) {
	var n uint32
	if r, _, err := procGetCapabilitiesStringLength.Call(uintptr(h), uintptr(unsafe.Pointer(&n))); r == 0 {
		return "", fmt.Errorf("GetCapabilitiesStringLength: %w", err)
	}
	if n == 0 || n > maxCapabilities {
		return "", fmt.Errorf("capability string of %d bytes", n)
	}
	buf := make([]byte, n)
	if r, _, err := procCapabilitiesRequestAndCapabilitiesReply.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(n)); r == 0 {
		return "", fmt.Errorf("CapabilitiesRequestAndCapabilitiesReply: %w", err)
	}
	for i, c := range buf {
		if c == 0 {
			buf = buf[:i]
			break
		}
	}
	return string(buf), nil
}

// enumMu guards enumFound: EnumDisplayMonitors calls back synchronously,
// and the callback is made once per process (there is a limit to them).
var (
	enumMu    sync.Mutex
	enumFound []uintptr
	enumCB    = sync.OnceValue(func() uintptr {
		return windows.NewCallback(func(hmon, _, _, _ uintptr) uintptr {
			enumFound = append(enumFound, hmon)
			return 1
		})
	})
)

// displayMonitors lists the HMONITORs of the desktop.
func displayMonitors() ([]uintptr, error) {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumFound = nil
	if r, _, err := procEnumDisplayMonitors.Call(0, 0, enumCB(), 0); r == 0 {
		return nil, fmt.Errorf("EnumDisplayMonitors: %w", err)
	}
	return append([]uintptr(nil), enumFound...), nil
}

// enumDisplays opens every physical monitor of the desktop. close
// releases the handles; it is safe to call when the list is empty.
func enumDisplays() ([]ddcDisplay, func(), error) {
	hmons, err := displayMonitors()
	if err != nil {
		return nil, func() {}, err
	}
	names := displayNames()
	var (
		out    []ddcDisplay
		opened [][]physicalMonitor
	)
	closeAll := func() {
		for _, pms := range opened {
			procDestroyPhysicalMonitors.Call(uintptr(len(pms)), uintptr(unsafe.Pointer(&pms[0])))
		}
	}
	for _, hmon := range hmons {
		mi := monitorInfoEx{cbSize: uint32(unsafe.Sizeof(monitorInfoEx{}))}
		if r, _, _ := procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r == 0 {
			continue
		}
		var n uint32
		if r, _, _ := procGetNumberOfPhysicalMonitorsFromHMONITOR.Call(hmon, uintptr(unsafe.Pointer(&n))); r == 0 || n == 0 || n > MaxMonitors {
			continue
		}
		pms := make([]physicalMonitor, n)
		if r, _, _ := procGetPhysicalMonitorsFromHMONITOR.Call(hmon, uintptr(n), uintptr(unsafe.Pointer(&pms[0]))); r == 0 {
			continue
		}
		opened = append(opened, pms)
		gdi := windows.UTF16ToString(mi.device[:])
		for i, pm := range pms {
			name := windows.UTF16ToString(pm.description[:])
			if friendly := names[gdi]; i < len(friendly) && friendly[i] != "" {
				name = friendly[i]
			}
			out = append(out, ddcDisplay{
				name:    name,
				primary: mi.flags&monitorInfoPrimary != 0 && i == 0,
				left:    mi.monitor.left,
				top:     mi.monitor.top,
				h:       dxvaMonitor(pm.handle),
			})
		}
	}
	return out, closeAll, nil
}

// displayNames maps a GDI device name ("\\.\DISPLAY1") to the friendly
// names of the monitors showing it, in path order. Best effort: nil when
// Windows will not say.
func displayNames() map[string][]string {
	paths, err := activePaths()
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, p := range paths {
		src := sourceDeviceName{header: deviceInfoHeader{
			typ: displayConfigGetSourceName, size: uint32(unsafe.Sizeof(sourceDeviceName{})),
			adapter: p.sourceAdapter, id: p.sourceID,
		}}
		if r, _, _ := procDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&src))); r != 0 {
			continue
		}
		tgt := targetDeviceName{header: deviceInfoHeader{
			typ: displayConfigGetTargetName, size: uint32(unsafe.Sizeof(targetDeviceName{})),
			adapter: p.targetAdapter, id: p.targetID,
		}}
		name := ""
		if r, _, _ := procDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&tgt))); r == 0 {
			name = windows.UTF16ToString(tgt.friendlyName[:])
		}
		gdi := windows.UTF16ToString(src.gdi[:])
		out[gdi] = append(out[gdi], name)
	}
	return out
}

// activePaths is QueryDisplayConfig(QDC_ONLY_ACTIVE_PATHS), retried while
// the layout changes between the size query and the query itself.
func activePaths() ([]pathInfo, error) {
	for range 3 {
		var np, nm uint32
		if r, _, _ := procGetDisplayConfigBufferSizes.Call(qdcOnlyActivePaths,
			uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&nm))); r != 0 {
			return nil, fmt.Errorf("GetDisplayConfigBufferSizes: %w", windows.Errno(r))
		}
		if np == 0 {
			return nil, nil
		}
		paths := make([]pathInfo, np)
		modes := make([]modeInfo, max(nm, 1))
		r, _, _ := procQueryDisplayConfig.Call(qdcOnlyActivePaths,
			uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&paths[0])),
			uintptr(unsafe.Pointer(&nm)), uintptr(unsafe.Pointer(&modes[0])), 0)
		switch windows.Errno(r) {
		case 0:
			return paths[:np], nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			continue
		}
		return nil, fmt.Errorf("QueryDisplayConfig: %w", windows.Errno(r))
	}
	return nil, errors.New("QueryDisplayConfig: the display layout kept changing")
}
