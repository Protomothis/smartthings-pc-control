package gui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// protocolApp answers which app opens links of a URL protocol (#137) for
// the user running the app: AssocQueryString reads HKCU and HKLM the way
// ShellExecute will. ok is false when no app has registered the protocol;
// name is the app's friendly name ("Steam"), "" when it has none. A
// variable so the tests need no registry.
var protocolApp = func(scheme string) (name string, ok bool) {
	const (
		assocstrCommand         = 1
		assocstrFriendlyAppName = 4
	)
	if name, ok := assocQuery(scheme, assocstrFriendlyAppName); ok && name != "" {
		return name, true
	}
	// Registered without a friendly name: the open command still says so.
	_, ok = assocQuery(scheme, assocstrCommand)
	return "", ok
}

var procAssocQueryString = windows.NewLazySystemDLL("shlwapi.dll").NewProc("AssocQueryStringW")

// assocQuery is AssocQueryStringW for a protocol's "open" verb.
func assocQuery(scheme string, str uint32) (string, bool) {
	const (
		assocfNoTruncate        = 0x00000020
		assocfInitIgnoreUnknown = 0x00000400
		assocfIsProtocol        = 0x00001000
	)
	assoc, err := windows.UTF16PtrFromString(scheme)
	if err != nil {
		return "", false
	}
	verb, _ := windows.UTF16PtrFromString("open")
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	hr, _, _ := procAssocQueryString.Call(
		assocfNoTruncate|assocfInitIgnoreUnknown|assocfIsProtocol,
		uintptr(str),
		uintptr(unsafe.Pointer(assoc)),
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)),
	)
	if hr != 0 {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}
