// Package privacy is the opt-in camera and microphone in-use detection
// (#143, docs/design/v1.3-sensors.md) and the verdict the shutdown guard
// acts on.
//
// Windows records every use of a capability in the user's consent store:
//
//	HKCU\Software\Microsoft\Windows\CurrentVersion\CapabilityAccessManager\
//	    ConsentStore\{webcam,microphone}\<package app>          LastUsedTimeStart/Stop
//	    ConsentStore\{webcam,microphone}\NonPackaged\<exe path>  LastUsedTimeStart/Stop
//
// An entry whose LastUsedTimeStop is 0 while LastUsedTimeStart is not is
// using the device right now. The store is per user, so the service
// (LocalSystem) reads HKU\<SID> of the target session's user; a hive that
// is not loaded, or any registry error, is "unknown" rather than a guess.
//
// The key names are app identities (package names, exe paths). They are
// read here and dropped: nothing but the two states and a time leaves the
// package, and nothing about the apps is logged.
package privacy

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/status"
)

// Device states, the wire values of the status block.
const (
	InUse   = "in_use"
	Idle    = "idle"
	Unknown = "unknown"
)

// Device names, as the guard reports them (409 devices, notifications).
const (
	Camera     = "camera"
	Microphone = "microphone"
)

// storeRoot is the consent store under a user's hive root.
const storeRoot = `Software\Microsoft\Windows\CurrentVersion\CapabilityAccessManager\ConsentStore\`

// capability is the consent store key of each device.
var capability = map[string]string{Camera: "webcam", Microphone: "microphone"}

// nonPackaged is the subkey that holds the desktop (Win32) apps.
const nonPackaged = "NonPackaged"

// ErrNotFound is what a Hive answers for a key that does not exist.
var ErrNotFound = errors.New("registry key not found")

// Hive is one user's registry hive, read-only. Paths are relative to the
// hive root (HKU\<SID>). The real one is OpenUserHive; tests use a map.
type Hive interface {
	// SubKeys lists the names of path's subkeys; ErrNotFound when path
	// does not exist.
	SubKeys(path string) ([]string, error)
	// Times reads path's LastUsedTimeStart and LastUsedTimeStop (FILETIME
	// QWORDs); a missing value reads as 0.
	Times(path string) (start, stop uint64, err error)
	Close() error
}

// Reading is one scan of both devices.
type Reading struct {
	Camera     string
	Microphone string
	// Since: while a device is in use, the start of the earliest use still
	// going on; otherwise the end of the last use. Zero when unknown.
	Since time.Time
}

// UnknownReading is the answer before a scan and whenever the hive cannot
// be read.
func UnknownReading() Reading { return Reading{Camera: Unknown, Microphone: Unknown} }

// InUseDevices names the devices in use, camera first; nil when none.
func (r Reading) InUseDevices() []string {
	var out []string
	if r.Camera == InUse {
		out = append(out, Camera)
	}
	if r.Microphone == InUse {
		out = append(out, Microphone)
	}
	return out
}

// SameState reports whether the two readings have the same device states
// (Since aside): the change the scanner reports.
func (r Reading) SameState(o Reading) bool {
	return r.Camera == o.Camera && r.Microphone == o.Microphone
}

// Wire is the status block, Since in loc.
func (r Reading) Wire(loc *time.Location) status.Privacy {
	out := status.Privacy{Camera: r.Camera, Microphone: r.Microphone}
	if !r.Since.IsZero() {
		if loc == nil {
			loc = time.Local
		}
		out.Since = r.Since.In(loc).Format(time.RFC3339)
	}
	return out
}

// LogLine describes a reading without anything about the apps.
func (r Reading) LogLine() string {
	return fmt.Sprintf("camera %s, microphone %s", r.Camera, r.Microphone)
}

// fileTimeEpoch is 1601-01-01 to 1970-01-01 in 100 ns units.
const fileTimeEpoch = 116444736000000000

// fileTime converts a FILETIME QWORD; 0 (or anything before 1970) is zero.
func fileTime(ft uint64) time.Time {
	if ft <= fileTimeEpoch {
		return time.Time{}
	}
	return time.Unix(0, int64(ft-fileTimeEpoch)*100)
}

// device is one capability's state and its time (see Reading.Since).
type device struct {
	state string
	at    time.Time
}

// readDevice scans one capability's entries in h. A missing capability key
// means nothing ever asked for the device: idle. A failure to list it is
// unknown; an entry that cannot be read is skipped.
func readDevice(h Hive, cap string) device {
	base := storeRoot + cap
	keys, err := h.SubKeys(base)
	if errors.Is(err, ErrNotFound) {
		return device{state: Idle}
	}
	if err != nil {
		return device{state: Unknown}
	}
	var start, stop uint64 // earliest live start, latest stop
	inUse := false
	check := func(path string) {
		s, e, err := h.Times(path)
		if err != nil {
			return
		}
		if s != 0 && e == 0 {
			if !inUse || s < start {
				start = s
			}
			inUse = true
			return
		}
		if e > stop {
			stop = e
		}
	}
	for _, k := range keys {
		if strings.EqualFold(k, nonPackaged) {
			apps, err := h.SubKeys(base + `\` + k)
			if err != nil {
				continue
			}
			for _, app := range apps {
				check(base + `\` + k + `\` + app)
			}
			continue
		}
		check(base + `\` + k)
	}
	if inUse {
		return device{state: InUse, at: fileTime(start)}
	}
	return device{state: Idle, at: fileTime(stop)}
}

// Read scans both devices in h.
func Read(h Hive) Reading {
	cam := readDevice(h, capability[Camera])
	mic := readDevice(h, capability[Microphone])
	r := Reading{Camera: cam.state, Microphone: mic.state}
	devs := []device{cam, mic}
	// In use: the earliest start among the devices in use.
	for _, d := range devs {
		if d.state == InUse && !d.at.IsZero() && (r.Since.IsZero() || d.at.Before(r.Since)) {
			r.Since = d.at
		}
	}
	if len(r.InUseDevices()) > 0 {
		return r
	}
	// Otherwise the latest end of a use.
	for _, d := range devs {
		if d.state == Idle && d.at.After(r.Since) {
			r.Since = d.at
		}
	}
	return r
}

// Monitor keeps the newest reading for the target user. Its funcs are
// called on every scan, so a test (or a changed target session) takes
// effect at once.
type Monitor struct {
	// User returns the SID whose hive is read: the target session's user.
	User func() (string, error)
	// Open opens that user's hive (OpenUserHive).
	Open func(sid string) (Hive, error)

	mu  sync.Mutex
	cur Reading
	set bool // cur holds a scan (since the start or the last Reset)
}

// read is one scan, unknown when the user or the hive is not there.
func (m *Monitor) read() Reading {
	if m.User == nil || m.Open == nil {
		return UnknownReading()
	}
	sid, err := m.User()
	if err != nil || sid == "" {
		return UnknownReading()
	}
	h, err := m.Open(sid)
	if err != nil {
		return UnknownReading()
	}
	defer h.Close()
	return Read(h)
}

// Scan reads the store and keeps the result; changed reports whether a
// device state differs from the previous reading (the unknown one before
// the first scan). A Since that moved on its own is not a change.
func (m *Monitor) Scan() (Reading, bool) {
	next := m.read()
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.cur
	if !m.set {
		prev = UnknownReading()
	}
	m.cur, m.set = next, true
	return next, !next.SameState(prev)
}

// Current is the newest reading, unknown before the first scan.
func (m *Monitor) Current() Reading {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.set {
		return UnknownReading()
	}
	return m.cur
}

// Reset forgets the reading (the option was turned off).
func (m *Monitor) Reset() {
	m.mu.Lock()
	m.cur, m.set = Reading{}, false
	m.mu.Unlock()
}

// ---- the guard -------------------------------------------------------------

// Verdict is what the guard says about one power command.
type Verdict struct {
	// InUse names the devices in use ("camera", "microphone"); nil when
	// none is, or when the guard does not apply.
	InUse []string
	// Block refuses the command (guard "block").
	Block bool
}

// Warn reports whether the command goes ahead with a warning.
func (v Verdict) Warn() bool { return len(v.InUse) > 0 && !v.Block }

// Devices is InUse joined with commas ("camera,microphone"), the form the
// notification fields carry.
func (v Verdict) Devices() string { return strings.Join(v.InUse, ",") }

// Judge applies guard ("off", "warn", "block") to a reading.
func Judge(guard string, r Reading) Verdict {
	if guard != config.PrivacyGuardWarn && guard != config.PrivacyGuardBlock {
		return Verdict{}
	}
	in := r.InUseDevices()
	if len(in) == 0 {
		return Verdict{}
	}
	return Verdict{InUse: in, Block: guard == config.PrivacyGuardBlock}
}

// DeviceInUseError is a power command the guard refused. The front ends
// answer it as 409 device_in_use.
type DeviceInUseError struct {
	Command string
	Devices []string
}

func (e *DeviceInUseError) Error() string {
	return "device_in_use: " + strings.Join(e.Devices, ",") + " in use, " + e.Command + " refused"
}

// ErrorCode is the wire reason of a refusal.
const ErrorCode = "device_in_use"
