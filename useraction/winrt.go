package useraction

// A minimal WinRT binding for the system media session (#117): just enough
// of Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager
// to find the current session, read what it plays and send it play, pause,
// stop, next and previous. Like coreaudio.go it calls the vtables directly
// with syscall.SyscallN; no WinRT projection library is involved.
//
// WinRT objects are COM objects whose interfaces derive from IInspectable
// (IUnknown plus GetIids, GetRuntimeClassName and GetTrustLevel), so an
// interface's own methods start at vtable slot 6. A runtime class travels
// in the ABI as its default interface, which is why the pointers GetResults
// and GetCurrentSession hand back can be called without a QueryInterface.
// The slot numbers below follow the method order of Windows.Media.winmd.
//
// Asynchronous methods return an IAsyncOperation<T>. Instead of a
// completion delegate — which would mean implementing a COM object in Go —
// await polls the operation's IAsyncInfo.Status with a bounded wait; the
// operations used here finish in a few milliseconds.
//
// Every call must happen inside withCOM (a locked thread with COM in the
// MTA, which is all RoInitialize(RO_INIT_MULTITHREADED) would set up).

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modCombase                    = windows.NewLazySystemDLL("combase.dll")
	procRoGetActivationFactory    = modCombase.NewProc("RoGetActivationFactory")
	procWindowsCreateString       = modCombase.NewProc("WindowsCreateString")
	procWindowsDeleteString       = modCombase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuffer = modCombase.NewProc("WindowsGetStringRawBuffer")
)

const gsmtcManagerClass = "Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager"

var (
	// IGlobalSystemMediaTransportControlsSessionManagerStatics
	iidGSMTCManagerStatics = windows.GUID{Data1: 0x2050C4EE, Data2: 0x11A0, Data3: 0x57DE, Data4: [8]byte{0xAE, 0xD7, 0xC9, 0x7C, 0x70, 0x33, 0x82, 0x45}}
	// IAsyncInfo
	iidIAsyncInfo = windows.GUID{Data1: 0x00000036, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
)

// Vtable slots. IUnknown is 0..2, IInspectable 3..5.
const (
	slotQueryInterface = 0

	// IGlobalSystemMediaTransportControlsSessionManagerStatics
	slotRequestAsync = 6
	// IAsyncInfo
	slotAsyncGetStatus    = 7
	slotAsyncGetErrorCode = 8
	slotAsyncCancel       = 9
	// IAsyncOperation<T>: put_Completed, get_Completed, GetResults
	slotAsyncGetResults = 8
	// IGlobalSystemMediaTransportControlsSessionManager
	slotGetCurrentSession = 6
	// IGlobalSystemMediaTransportControlsSession
	slotGetSourceAppUserModelID    = 6
	slotTryGetMediaPropertiesAsync = 7
	slotGetPlaybackInfo            = 9
	slotTryPlayAsync               = 10
	slotTryPauseAsync              = 11
	slotTryStopAsync               = 12
	slotTrySkipNextAsync           = 16
	slotTrySkipPreviousAsync       = 17
	slotTryTogglePlayPauseAsync    = 20
	// IGlobalSystemMediaTransportControlsSessionMediaProperties
	slotGetTitle      = 6
	slotGetArtist     = 9
	slotGetAlbumTitle = 10
	// IGlobalSystemMediaTransportControlsSessionPlaybackInfo
	slotGetPlaybackStatus = 7
)

// sessionControlSlot is the Try…Async method of each media verb.
var sessionControlSlot = map[string]int{
	"play":      slotTryPlayAsync,
	"pause":     slotTryPauseAsync,
	"playpause": slotTryTogglePlayPauseAsync,
	"stop":      slotTryStopAsync,
	"next":      slotTrySkipNextAsync,
	"prev":      slotTrySkipPreviousAsync,
}

// AsyncStatus values of IAsyncInfo.Status.
const (
	asyncStarted   = 0
	asyncCompleted = 1
	asyncCanceled  = 2
	asyncError     = 3
)

// GlobalSystemMediaTransportControlsSessionPlaybackStatus.
const (
	gsmtcClosed   = 0
	gsmtcOpened   = 1
	gsmtcChanging = 2
	gsmtcStopped  = 3
	gsmtcPlaying  = 4
	gsmtcPaused   = 5
)

// HRESULTs that mean "this Windows has no such API" rather than a failure.
const (
	regdbEClassNotReg = 0x80040154 // REGDB_E_CLASSNOTREG
	eNoInterface      = 0x80004002 // E_NOINTERFACE
)

const (
	// asyncPoll is how often await looks at an operation.
	asyncPoll = 5 * time.Millisecond
	// sessionManagerTimeout bounds RequestAsync, sessionCallTimeout every
	// other operation. Together they stay well inside the service's 3s
	// budget for one user-action run.
	sessionManagerTimeout = 1500 * time.Millisecond
	sessionCallTimeout    = 1000 * time.Millisecond
)

// errNoMediaSessionAPI is returned when the session manager cannot be
// activated at all: Windows before 10 1809, or a stripped-down edition.
var errNoMediaSessionAPI = errors.New("the Windows media session API is not available")

// hstring is an HSTRING handle; 0 is the empty string.
type hstring uintptr

func newHString(s string) (hstring, error) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h hstring
	hr, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	if err := check("WindowsCreateString", uint32(hr)); err != nil {
		return 0, err
	}
	return h, nil
}

// String copies the characters out; the handle stays owned by the caller.
func (h hstring) String() string {
	if h == 0 {
		return ""
	}
	var n uint32
	p, _, _ := procWindowsGetStringRawBuffer.Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
	if p == 0 || n == 0 {
		return ""
	}
	// Reinterpret the returned address as a pointer without a
	// uintptr→unsafe.Pointer conversion (which vet rightly distrusts).
	buf := *(**uint16)(unsafe.Pointer(&p))
	return windows.UTF16ToString(unsafe.Slice(buf, n))
}

func (h hstring) free() {
	if h != 0 {
		procWindowsDeleteString.Call(uintptr(h))
	}
}

// getString calls a property getter that returns an HSTRING.
func getString(o *comObject, slot int, op string) (string, error) {
	var h hstring
	if err := check(op, o.call(slot, uintptr(unsafe.Pointer(&h)))); err != nil {
		return "", err
	}
	defer h.free()
	return h.String(), nil
}

// await waits for an IAsyncOperation to finish, at most timeout. A
// timed-out operation is cancelled.
func await(op *comObject, name string, timeout time.Duration) error {
	var info *comObject
	if err := check(name+": QueryInterface(IAsyncInfo)",
		op.call(slotQueryInterface, uintptr(unsafe.Pointer(&iidIAsyncInfo)), uintptr(unsafe.Pointer(&info)))); err != nil {
		return err
	}
	defer info.release()
	deadline := time.Now().Add(timeout)
	for {
		var status int32
		if err := check(name+": IAsyncInfo.Status", info.call(slotAsyncGetStatus, uintptr(unsafe.Pointer(&status)))); err != nil {
			return err
		}
		switch status {
		case asyncCompleted:
			return nil
		case asyncCanceled:
			return fmt.Errorf("%s: cancelled", name)
		case asyncError:
			var hr int32
			info.call(slotAsyncGetErrorCode, uintptr(unsafe.Pointer(&hr)))
			return &hresultError{op: name, hr: uint32(hr)}
		}
		if time.Now().After(deadline) {
			info.call(slotAsyncCancel)
			return fmt.Errorf("%s: no result within %v", name, timeout)
		}
		time.Sleep(asyncPoll)
	}
}

// awaitObject runs an async method that yields an object and returns it.
func awaitObject(o *comObject, slot int, name string, timeout time.Duration) (*comObject, error) {
	var op *comObject
	if err := check(name, o.call(slot, uintptr(unsafe.Pointer(&op)))); err != nil {
		return nil, err
	}
	defer op.release()
	if err := await(op, name, timeout); err != nil {
		return nil, err
	}
	var result *comObject
	if err := check(name+": GetResults", op.call(slotAsyncGetResults, uintptr(unsafe.Pointer(&result)))); err != nil {
		return nil, err
	}
	return result, nil
}

// awaitBool runs an async method that yields a boolean.
func awaitBool(o *comObject, slot int, name string, timeout time.Duration) (bool, error) {
	var op *comObject
	if err := check(name, o.call(slot, uintptr(unsafe.Pointer(&op)))); err != nil {
		return false, err
	}
	defer op.release()
	if err := await(op, name, timeout); err != nil {
		return false, err
	}
	var ok uint8 // WinRT boolean is one byte
	if err := check(name+": GetResults", op.call(slotAsyncGetResults, uintptr(unsafe.Pointer(&ok)))); err != nil {
		return false, err
	}
	return ok != 0, nil
}

// gsmtcSession is the current session. It implements mediaSession; open
// and use it inside one withCOM call.
type gsmtcSession struct {
	obj   *comObject // IGlobalSystemMediaTransportControlsSession
	aumid string
}

// openCurrentSession asks the session manager for the session Windows
// currently shows in its media flyout. It returns nil and no error when
// nothing is playing or paused anywhere.
func openCurrentSession() (mediaSession, error) {
	if err := procRoGetActivationFactory.Find(); err != nil {
		return nil, errNoMediaSessionAPI
	}
	class, err := newHString(gsmtcManagerClass)
	if err != nil {
		return nil, err
	}
	defer class.free()
	var statics *comObject
	hr, _, _ := procRoGetActivationFactory.Call(uintptr(class), uintptr(unsafe.Pointer(&iidGSMTCManagerStatics)), uintptr(unsafe.Pointer(&statics)))
	if uint32(hr) == regdbEClassNotReg || uint32(hr) == eNoInterface {
		return nil, errNoMediaSessionAPI
	}
	if err := check("RoGetActivationFactory(SessionManager)", uint32(hr)); err != nil {
		return nil, err
	}
	defer statics.release()

	mgr, err := awaitObject(statics, slotRequestAsync, "SessionManager.RequestAsync", sessionManagerTimeout)
	if err != nil {
		return nil, err
	}
	defer mgr.release()

	var sess *comObject
	if err := check("GetCurrentSession", mgr.call(slotGetCurrentSession, uintptr(unsafe.Pointer(&sess)))); err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, nil
	}
	// Best effort: a session without an id still has a status and controls.
	aumid, _ := getString(sess, slotGetSourceAppUserModelID, "SourceAppUserModelId")
	return &gsmtcSession{obj: sess, aumid: aumid}, nil
}

func (s *gsmtcSession) AppID() string { return s.aumid }

func (s *gsmtcSession) Status() (string, error) {
	var info *comObject
	if err := check("GetPlaybackInfo", s.obj.call(slotGetPlaybackInfo, uintptr(unsafe.Pointer(&info)))); err != nil {
		return "", err
	}
	defer info.release()
	var st int32
	if err := check("PlaybackStatus", info.call(slotGetPlaybackStatus, uintptr(unsafe.Pointer(&st)))); err != nil {
		return "", err
	}
	return playbackStatusName(st), nil
}

// playbackStatusName maps the WinRT enum to the wire values. A session
// that is open but not playing (Opened, and Changing between tracks) reads
// as stopped; Closed is a session on its way out, so nothing plays.
func playbackStatusName(st int32) string {
	switch st {
	case gsmtcPlaying:
		return MediaPlaying
	case gsmtcPaused:
		return MediaPaused
	case gsmtcStopped, gsmtcOpened, gsmtcChanging:
		return MediaStopped
	}
	return MediaNone
}

func (s *gsmtcSession) Properties() (title, artist, album string, err error) {
	props, err := awaitObject(s.obj, slotTryGetMediaPropertiesAsync, "TryGetMediaPropertiesAsync", sessionCallTimeout)
	if err != nil {
		return "", "", "", err
	}
	defer props.release()
	if title, err = getString(props, slotGetTitle, "Title"); err != nil {
		return "", "", "", err
	}
	artist, _ = getString(props, slotGetArtist, "Artist")
	album, _ = getString(props, slotGetAlbumTitle, "AlbumTitle")
	return title, artist, album, nil
}

func (s *gsmtcSession) Control(verb string) (bool, error) {
	slot, ok := sessionControlSlot[verb]
	if !ok {
		return false, nil
	}
	return awaitBool(s.obj, slot, "Session."+verb, sessionCallTimeout)
}

func (s *gsmtcSession) Close() {
	s.obj.release()
	s.obj = nil
}
