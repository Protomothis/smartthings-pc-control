package appid

// The Start menu shortcut is written and read with the shell's own
// ShellLink object: IShellLinkW for target, arguments and working folder,
// IPropertyStore for System.AppUserModel.ID, IPersistFile to load and save
// the .lnk. The vtables are called directly with syscall.SyscallN, like
// useraction/coreaudio.go; no PowerShell (whose Add-Type start alone takes
// about a second) and no WScript.Shell (which cannot set the AUMID).
//
// Every COM call happens inside withCOM, on one locked OS thread.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Result says what EnsureShortcut did.
type Result string

const (
	// Unchanged: the shortcut was already correct; nothing was written.
	Unchanged Result = "unchanged"
	// Created: there was no shortcut.
	Created Result = "created"
	// Repaired: a shortcut was there but pointed elsewhere or carried a
	// different (or no) AUMID, and was rewritten.
	Repaired Result = "repaired"
)

var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
	procPropVariantClear = modOle32.NewProc("PropVariantClear")
)

var (
	clsidShellLink    = windows.GUID{Data1: 0x00021401, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIShellLinkW    = windows.GUID{Data1: 0x000214F9, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIPersistFile   = windows.GUID{Data1: 0x0000010B, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIPropertyStore = windows.GUID{Data1: 0x886D8EEB, Data2: 0x8CF2, Data3: 0x4446, Data4: [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}
	// pkeyAppUserModelID is PKEY_AppUserModel_ID (System.AppUserModel.ID).
	pkeyAppUserModelID = propertyKey{
		fmtid: windows.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39, Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}},
		pid:   5,
	}
)

const (
	clsctxInprocServer = 0x1
	coinitSTA          = 0x2 // COINIT_APARTMENTTHREADED
	coinitNoOLE1DDE    = 0x4 // COINIT_DISABLE_OLE1DDE
	rpcEChanged        = 0x80010106
	sFalse             = 1
	stgmRead           = 0
	vtLPWSTR           = 31
	textBuf            = 1024 // UTF-16 units for GetPath/GetArguments/GetWorkingDirectory
)

// Vtable slots; IUnknown's QueryInterface/AddRef/Release are 0..2.
const (
	slotQueryInterface = 0
	slotRelease        = 2

	// IShellLinkW
	slotGetPath             = 3
	slotSetDescription      = 7
	slotGetWorkingDirectory = 8
	slotSetWorkingDirectory = 9
	slotGetArguments        = 10
	slotSetArguments        = 11
	slotSetIconLocation     = 17
	slotSetPath             = 20
	// IPersistFile (IPersist's GetClassID is 3)
	slotLoad = 5
	slotSave = 6
	// IPropertyStore
	slotGetValue = 5
	slotSetValue = 6
	slotCommit   = 7
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is PROPVARIANT; only VT_LPWSTR is used, whose value is the
// first pointer of the union.
type propVariant struct {
	vt  uint16
	_   [3]uint16
	val *uint16
	_   uintptr
}

// comObject is an interface pointer: a pointer to a pointer to its vtable.
type comObject struct {
	vtbl *[32]uintptr
}

// fn is the function in vtable slot i. Callers pass it straight to
// syscall.SyscallN so that pointer arguments are converted in the call
// expression itself (and kept alive for the call).
func (o *comObject) fn(slot int) uintptr { return o.vtbl[slot] }

func (o *comObject) release() {
	if o != nil {
		syscall.SyscallN(o.fn(slotRelease), uintptr(unsafe.Pointer(o)))
	}
}

func (o *comObject) queryInterface(iid *windows.GUID, op string) (*comObject, error) {
	var out *comObject
	hr, _, _ := syscall.SyscallN(o.fn(slotQueryInterface), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if err := check(op, hr); err != nil {
		return nil, err
	}
	return out, nil
}

// check turns a failing HRESULT into an error; S_OK and S_FALSE pass.
func check(op string, hr uintptr) error {
	if int32(uint32(hr)) >= 0 {
		return nil
	}
	return fmt.Errorf("%s: HRESULT 0x%08X", op, uint32(hr))
}

// withCOM runs f on a locked OS thread inside a single-threaded apartment,
// the one shell objects are meant for. A thread Go reuses may already be
// in the other apartment (RPC_E_CHANGED_MODE); COM is usable there all the
// same, but that init is not ours to undo.
func withCOM(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr := uint32(0)
	if err := windows.CoInitializeEx(0, coinitSTA|coinitNoOLE1DDE); err != nil {
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

func newShellLink() (*comObject, error) {
	var link *comObject
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)),
		uintptr(unsafe.Pointer(&link)),
	)
	if err := check("CoCreateInstance(ShellLink)", hr); err != nil {
		return nil, err
	}
	return link, nil
}

// link is what a shortcut says, as far as this package cares.
type link struct {
	target, args, workDir, aumid string
}

// getText calls an IShellLinkW getter of the (buffer, size) shape, or
// GetPath, which also takes a WIN32_FIND_DATA pointer and flags (both 0).
func getText(o *comObject, slot int, op string) (string, error) {
	buf := make([]uint16, textBuf)
	var hr uintptr
	if slot == slotGetPath {
		hr, _, _ = syscall.SyscallN(o.fn(slot), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(&buf[0])), textBuf, 0, 0)
	} else {
		hr, _, _ = syscall.SyscallN(o.fn(slot), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(&buf[0])), textBuf)
	}
	if err := check(op, hr); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf), nil
}

func setText(o *comObject, slot int, op, s string) error {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	hr, _, _ := syscall.SyscallN(o.fn(slot), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(p)))
	return check(op, hr)
}

// readLink loads the shortcut at path. Call inside withCOM.
func readLink(path string) (link, error) {
	sl, err := newShellLink()
	if err != nil {
		return link{}, err
	}
	defer sl.release()
	pf, err := sl.queryInterface(&iidIPersistFile, "QueryInterface(IPersistFile)")
	if err != nil {
		return link{}, err
	}
	defer pf.release()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return link{}, err
	}
	hr, _, _ := syscall.SyscallN(pf.fn(slotLoad), uintptr(unsafe.Pointer(pf)), uintptr(unsafe.Pointer(p)), stgmRead)
	if err := check("IPersistFile.Load", hr); err != nil {
		return link{}, err
	}

	var l link
	if l.target, err = getText(sl, slotGetPath, "IShellLink.GetPath"); err != nil {
		return link{}, err
	}
	if l.args, err = getText(sl, slotGetArguments, "IShellLink.GetArguments"); err != nil {
		return link{}, err
	}
	if l.workDir, err = getText(sl, slotGetWorkingDirectory, "IShellLink.GetWorkingDirectory"); err != nil {
		return link{}, err
	}

	ps, err := sl.queryInterface(&iidIPropertyStore, "QueryInterface(IPropertyStore)")
	if err != nil {
		return link{}, err
	}
	defer ps.release()
	var pv propVariant
	hr, _, _ = syscall.SyscallN(ps.fn(slotGetValue), uintptr(unsafe.Pointer(ps)), uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&pv)))
	if err := check("IPropertyStore.GetValue(AppUserModel.ID)", hr); err != nil {
		return link{}, err
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	if pv.vt == vtLPWSTR && pv.val != nil {
		// The PROPVARIANT owns the string until PropVariantClear.
		l.aumid = windows.UTF16PtrToString(pv.val)
	}
	return l, nil
}

// writeLink (over)writes the shortcut at path. Call inside withCOM.
func writeLink(path string, want link) error {
	sl, err := newShellLink()
	if err != nil {
		return err
	}
	defer sl.release()
	for _, s := range []struct {
		slot    int
		op, val string
	}{
		{slotSetPath, "IShellLink.SetPath", want.target},
		{slotSetArguments, "IShellLink.SetArguments", want.args},
		{slotSetWorkingDirectory, "IShellLink.SetWorkingDirectory", want.workDir},
		{slotSetDescription, "IShellLink.SetDescription", DisplayName},
	} {
		if err := setText(sl, s.slot, s.op, s.val); err != nil {
			return err
		}
	}
	icon, err := windows.UTF16PtrFromString(want.target)
	if err != nil {
		return err
	}
	hr, _, _ := syscall.SyscallN(sl.fn(slotSetIconLocation), uintptr(unsafe.Pointer(sl)), uintptr(unsafe.Pointer(icon)), 0)
	if err := check("IShellLink.SetIconLocation", hr); err != nil {
		return err
	}

	ps, err := sl.queryInterface(&iidIPropertyStore, "QueryInterface(IPropertyStore)")
	if err != nil {
		return err
	}
	defer ps.release()
	id, err := windows.UTF16PtrFromString(want.aumid)
	if err != nil {
		return err
	}
	// SetValue copies the value, so a Go-owned string is fine here.
	pv := propVariant{vt: vtLPWSTR, val: id}
	hr, _, _ = syscall.SyscallN(ps.fn(slotSetValue), uintptr(unsafe.Pointer(ps)), uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&pv)))
	runtime.KeepAlive(id)
	if err := check("IPropertyStore.SetValue(AppUserModel.ID)", hr); err != nil {
		return err
	}
	hr, _, _ = syscall.SyscallN(ps.fn(slotCommit), uintptr(unsafe.Pointer(ps)))
	if err := check("IPropertyStore.Commit", hr); err != nil {
		return err
	}

	pf, err := sl.queryInterface(&iidIPersistFile, "QueryInterface(IPersistFile)")
	if err != nil {
		return err
	}
	defer pf.release()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	hr, _, _ = syscall.SyscallN(pf.fn(slotSave), uintptr(unsafe.Pointer(pf)), uintptr(unsafe.Pointer(p)), 1)
	return check("IPersistFile.Save", hr)
}

// longPath is p in its long, cleaned form, so "C:\Users\STREAM~1\x.exe"
// and "C:\Users\streamdeck\x.exe" compare equal. p is returned cleaned but
// otherwise as is when it does not exist.
func longPath(p string) string {
	p = filepath.Clean(p)
	src, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(src, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}

func samePath(a, b string) bool { return strings.EqualFold(longPath(a), longPath(b)) }

// matches reports whether have is the shortcut want describes.
func (have link) matches(want link) bool {
	return samePath(have.target, want.target) && have.args == want.args &&
		samePath(have.workDir, want.workDir) && have.aumid == want.aumid
}

// wantLink is the shortcut for exe.
func wantLink(exe string) link {
	return link{target: exe, args: shortcutArgs, workDir: filepath.Dir(exe), aumid: AUMID}
}

// EnsureShortcut makes dir\ShortcutFile start exe with the `gui` argument
// and carry AUMID. An existing shortcut that already does is only read,
// not written; one that points elsewhere, has other arguments or another
// (or no) AUMID, or cannot be read at all is rewritten.
func EnsureShortcut(dir, exe string) (Result, error) {
	if exe == "" {
		return "", errors.New("no target exe")
	}
	path := filepath.Join(dir, ShortcutFile)
	want := wantLink(exe)
	var res Result
	err := withCOM(func() error {
		res = Created
		if _, statErr := os.Stat(path); statErr == nil {
			res = Repaired
			if have, err := readLink(path); err == nil && have.matches(want) {
				res = Unchanged
				return nil
			}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return writeLink(path, want)
	})
	if err != nil {
		return "", fmt.Errorf("start menu shortcut: %w", err)
	}
	return res, nil
}

// RemoveShortcut deletes dir\ShortcutFile; a missing one is not an error.
func RemoveShortcut(dir string) error {
	err := os.Remove(filepath.Join(dir, ShortcutFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ProgramsDir is the calling user's Start menu Programs folder
// (FOLDERID_Programs, %APPDATA%\Microsoft\Windows\Start Menu\Programs).
// It is resolved from the process token, not from %APPDATA%: the
// user-action child runs under the user's token but with the service's
// (SYSTEM's) environment.
func ProgramsDir() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Programs, 0)
}

// EnsureToastShortcut is EnsureShortcut for the running exe in the
// calling user's Start menu.
func EnsureToastShortcut() (Result, error) {
	dir, err := ProgramsDir()
	if err != nil {
		return "", fmt.Errorf("start menu folder: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return EnsureShortcut(dir, exe)
}

// RemoveToastShortcut removes the calling user's shortcut, if any.
func RemoveToastShortcut() error {
	dir, err := ProgramsDir()
	if err != nil {
		return err
	}
	return RemoveShortcut(dir)
}
