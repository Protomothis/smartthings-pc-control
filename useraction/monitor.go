package useraction

// The monitor handler (#144): `user-action monitor get|set` reads and sets
// the monitors over DDC/CI (MCCS VCP codes) through dxva2.dll, bound in
// dxva2.go. It runs in the session on the physical console: an RDP
// session's displays are virtual and answer no DDC.
//
//	monitor get
//	  {"ok":true,"monitors":[{"id":"m1","name":"DELL U2720Q","primary":true,
//	    "ddc":true,"brightness":40,"input":"hdmi1","inputs":["dp1","hdmi1"],
//	    "power":"on"}]}
//	monitor set --id m1 --brightness 40|--input hdmi1|--power on|off
//	  {"ok":true,"monitor":{...}}   the monitor read back, "inputs" without
//	                                the capability string (too slow to ask
//	                                again; the service keeps the list)
//
// The ids number the monitors in a fixed order — the primary first, then
// left to right, top to bottom — so a `set` started from a `get` a minute
// earlier still means the same screen as long as the layout has not
// changed.
//
// The values (VCP):
//
//	0x10 brightness  current/maximum, shown as 0–100
//	0x60 input       MCCS input source: 0x0F dp1, 0x11 hdmi1, …; a value
//	                 MCCS does not name is "vcp:0xNN"
//	0xD6 power       1 on; 2–5 standby, suspend, off (DPM) and off
//	                 (hard): all "standby" here
//
// Turning a monitor off is 0xD6 = 4 (DPM off). Some monitors stop
// listening to DDC/CI once off, so "on" may then fail; that is reported,
// not hidden (docs/design/v1.3-sensors.md).

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// VCP codes (MCCS 2.2).
const (
	vcpBrightness = 0x10
	vcpInput      = 0x60
	vcpPower      = 0xD6

	vcpPowerOn  = 1
	vcpPowerOff = 4 // DPM off: the "off" the monitor's own standby uses
)

// Monitor power states of Monitor.Power.
const (
	MonitorPowerOn      = "on"
	MonitorPowerStandby = "standby"
	MonitorPowerUnknown = "unknown"
)

// Limits of a monitor reading.
const (
	// MaxMonitors bounds the list; nobody has more screens than this.
	MaxMonitors = 16
	// MaxMonitorInputs bounds a monitor's input list.
	MaxMonitorInputs = 32
)

// Monitor is one display as `monitor get` reads it, and the "monitors"
// entry of /st/v1/status (#144). Brightness and Input are null while the
// monitor does not answer them; Inputs is never null.
type Monitor struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Primary    bool     `json:"primary"`
	DDC        bool     `json:"ddc"`
	Brightness *int     `json:"brightness"`
	Input      *string  `json:"input"`
	Inputs     []string `json:"inputs"`
	Power      string   `json:"power"`
}

// Validate checks a reading before the service stores it.
func (m Monitor) Validate() error {
	if !ValidMonitorID(m.ID) {
		return fmt.Errorf("monitor id %q", m.ID)
	}
	if utf8.RuneCountInString(m.Name) > MaxDeviceRunes || hasControl(m.Name) {
		return errors.New("monitor name too long or with control characters")
	}
	if b := m.Brightness; b != nil && (*b < 0 || *b > 100) {
		return fmt.Errorf("brightness %d out of range 0-100", *b)
	}
	if m.Input != nil {
		if _, ok := MonitorInputCode(*m.Input); !ok {
			return fmt.Errorf("input %q", *m.Input)
		}
	}
	if len(m.Inputs) > MaxMonitorInputs {
		return fmt.Errorf("more than %d inputs", MaxMonitorInputs)
	}
	for _, in := range m.Inputs {
		if _, ok := MonitorInputCode(in); !ok {
			return fmt.Errorf("input %q", in)
		}
	}
	switch m.Power {
	case MonitorPowerOn, MonitorPowerStandby, MonitorPowerUnknown:
	default:
		return fmt.Errorf("power %q", m.Power)
	}
	return nil
}

// Clone copies the pointers and the list, so a stored reading is never
// aliased.
func (m Monitor) Clone() Monitor {
	if m.Brightness != nil {
		b := *m.Brightness
		m.Brightness = &b
	}
	if m.Input != nil {
		in := *m.Input
		m.Input = &in
	}
	m.Inputs = slices.Clone(m.Inputs)
	if m.Inputs == nil {
		m.Inputs = []string{}
	}
	return m
}

// MonitorRequest is the `monitor set` part of a Request: one of the three
// settings for one monitor.
type MonitorRequest struct {
	ID string
	// Setting is brightness, input or power.
	Setting string
	// Level is the brightness 0–100.
	Level int
	// Arg is the input name (hdmi1, vcp:0x1b) or the power state (on, off).
	Arg string
}

// MonitorSettings are the `monitor set` flags without their dashes.
var MonitorSettings = []string{"brightness", "input", "power"}

var monitorIDPattern = regexp.MustCompile(`^m[1-9][0-9]?$`)

// ValidMonitorID reports whether id is an id `monitor get` hands out: m1,
// m2, …
func ValidMonitorID(id string) bool { return monitorIDPattern.MatchString(id) }

func monitorID(i int) string { return "m" + strconv.Itoa(i+1) }

// monitorInputNames are the MCCS 2.2 input source values (VCP 0x60).
var monitorInputNames = map[byte]string{
	0x01: "vga1",
	0x02: "vga2",
	0x03: "dvi1",
	0x04: "dvi2",
	0x05: "composite1",
	0x06: "composite2",
	0x07: "svideo1",
	0x08: "svideo2",
	0x09: "tuner1",
	0x0A: "tuner2",
	0x0B: "tuner3",
	0x0C: "component1",
	0x0D: "component2",
	0x0E: "component3",
	0x0F: "dp1",
	0x10: "dp2",
	0x11: "hdmi1",
	0x12: "hdmi2",
}

var monitorInputCodes = func() map[string]byte {
	out := make(map[string]byte, len(monitorInputNames))
	for code, name := range monitorInputNames {
		out[name] = code
	}
	return out
}()

var rawInputPattern = regexp.MustCompile(`^vcp:0x[0-9a-f]{2}$`)

// MonitorInputName is the name of an input source value: the MCCS name,
// or "vcp:0xNN" for one MCCS does not name (USB-C on many monitors).
func MonitorInputName(code byte) string {
	if name, ok := monitorInputNames[code]; ok {
		return name
	}
	return fmt.Sprintf("vcp:0x%02x", code)
}

// MonitorInputCode is the value an input name stands for; false for
// anything MonitorInputName cannot produce (and for vcp:0x00, which MCCS
// leaves unused).
func MonitorInputCode(name string) (byte, bool) {
	if code, ok := monitorInputCodes[name]; ok {
		return code, true
	}
	if !rawInputPattern.MatchString(name) {
		return 0, false
	}
	n, err := strconv.ParseUint(name[len("vcp:0x"):], 16, 8)
	if err != nil || n == 0 {
		return 0, false
	}
	if named, ok := monitorInputNames[byte(n)]; ok && named != name {
		// One spelling per value: 0x11 is hdmi1, not vcp:0x11.
		return 0, false
	}
	return byte(n), true
}

// MonitorInputLabel is how the app and the bot show an input: "HDMI1",
// "DP1", "vcp:0x1b".
func MonitorInputLabel(name string) string {
	if strings.HasPrefix(name, "vcp:") {
		return name
	}
	return strings.ToUpper(name)
}

func parseMonitor(args []string) (Request, error) {
	req := Request{Action: ActionMonitor}
	if len(args) == 0 {
		return req, badArgs("monitor: missing verb (get, set)")
	}
	req.Verb = args[0]
	switch req.Verb {
	case "get":
		if len(args) != 1 {
			return req, badArgs("monitor get takes no arguments")
		}
		return req, nil
	case "set":
	default:
		return req, badArgs("monitor: unknown verb %q", req.Verb)
	}
	const usage = "usage: monitor set --id <mN> (--brightness <0-100> | --input <name> | --power <on|off>)"
	flags, err := flagSet{valued: map[string]bool{
		"--id": true, "--brightness": true, "--input": true, "--power": true,
	}}.parse(args[1:])
	if err != nil {
		return req, err
	}
	id, hasID := flags["--id"]
	if !hasID || len(flags) != 2 {
		return req, badArgs(usage)
	}
	if !ValidMonitorID(id[0]) {
		return req, badArgs("monitor set: %q is not a monitor id", id[0])
	}
	req.Monitor.ID = id[0]
	for _, setting := range MonitorSettings {
		v, ok := flags["--"+setting]
		if !ok {
			continue
		}
		req.Monitor.Setting = setting
		switch setting {
		case "brightness":
			n, ok := parseInt(v[0], false)
			if !ok || n < 0 || n > 100 {
				return req, badArgs("monitor set: %q is not a brightness 0-100", v[0])
			}
			req.Monitor.Level = n
		case "input":
			if _, ok := MonitorInputCode(v[0]); !ok {
				return req, badArgs("monitor set: %q is not an input name", v[0])
			}
			req.Monitor.Arg = v[0]
		case "power":
			if v[0] != "on" && v[0] != "off" {
				return req, badArgs("monitor set: power %q is not on or off", v[0])
			}
			req.Monitor.Arg = v[0]
		}
	}
	return req, nil
}

// ddcHandle is one physical monitor's DDC/CI channel, behind an interface
// so the handler is tested without a monitor (and never sends a real one
// anything from a test).
type ddcHandle interface {
	// GetVCP reads a VCP code: its current and maximum value.
	GetVCP(code byte) (cur, max uint32, err error)
	SetVCP(code byte, value uint32) error
	// Capabilities is the monitor's MCCS capability string.
	Capabilities() (string, error)
}

// ddcDisplay is one physical monitor as the enumeration found it.
type ddcDisplay struct {
	name      string
	primary   bool
	left, top int32
	h         ddcHandle
}

var (
	// openDisplays lists the physical monitors; close releases them. Tests
	// replace it.
	openDisplays = enumDisplays
	// ddcSettle is the pause after a set before the value is read back:
	// MCCS asks a host to wait 50 ms after a write.
	ddcSettle = 100 * time.Millisecond
)

func init() {
	Register(ActionMonitor, monitorHandler)
}

// monitorHandler serves `user-action monitor …`.
func monitorHandler(req Request) (map[string]any, error) {
	displays, closeAll, err := openDisplays()
	if err != nil {
		return nil, Failed("monitor: %v", err)
	}
	defer closeAll()
	orderDisplays(displays)
	if len(displays) > MaxMonitors {
		displays = displays[:MaxMonitors]
	}
	switch req.Verb {
	case "get":
		out := make([]Monitor, 0, len(displays))
		for i, d := range displays {
			out = append(out, readMonitor(monitorID(i), d, true))
		}
		return map[string]any{"monitors": out}, nil
	case "set":
		i, _ := strconv.Atoi(strings.TrimPrefix(req.Monitor.ID, "m"))
		if i < 1 || i > len(displays) {
			return nil, Unsupported("monitor %s not found (%d connected)", req.Monitor.ID, len(displays))
		}
		d := displays[i-1]
		if err := applyMonitor(d.h, req.Monitor); err != nil {
			return nil, err
		}
		if ddcSettle > 0 {
			time.Sleep(ddcSettle)
		}
		m := readMonitor(req.Monitor.ID, d, false)
		// A monitor busy switching (input, power) may not answer the read
		// back yet: report what it accepted.
		switch req.Monitor.Setting {
		case "input":
			if m.Input == nil {
				in := req.Monitor.Arg
				m.Input = &in
				m.Inputs = []string{in}
			}
		case "power":
			if req.Monitor.Arg == "off" && m.Power != MonitorPowerStandby {
				m.Power = MonitorPowerStandby
			} else if req.Monitor.Arg == "on" && m.Power == MonitorPowerUnknown {
				m.Power = MonitorPowerOn
			}
		}
		m.DDC = true
		return map[string]any{"monitor": m}, nil
	}
	return nil, badArgs("monitor: unknown verb %q", req.Verb)
}

// orderDisplays sorts the monitors into id order: the primary first, then
// left to right, top to bottom; the enumeration order breaks ties.
func orderDisplays(ds []ddcDisplay) {
	slices.SortStableFunc(ds, func(a, b ddcDisplay) int {
		switch {
		case a.primary != b.primary:
			if a.primary {
				return -1
			}
			return 1
		case a.left != b.left:
			return int(a.left) - int(b.left)
		}
		return int(a.top) - int(b.top)
	})
}

// applyMonitor sends one setting. A monitor that does not even answer the
// brightness read is "unsupported"; a write the monitor refused is
// "failed".
func applyMonitor(h ddcHandle, r MonitorRequest) error {
	switch r.Setting {
	case "brightness":
		_, maxv, err := h.GetVCP(vcpBrightness)
		if err != nil {
			return Unsupported("this monitor does not answer DDC/CI brightness: %v", err)
		}
		if maxv == 0 {
			return Unsupported("this monitor reports no brightness range")
		}
		v := uint32(math.Round(float64(r.Level) * float64(maxv) / 100))
		if err := h.SetVCP(vcpBrightness, v); err != nil {
			return Failed("the monitor refused the brightness: %v", err)
		}
	case "input":
		code, ok := MonitorInputCode(r.Arg)
		if !ok {
			return badArgs("monitor set: %q is not an input name", r.Arg)
		}
		if err := h.SetVCP(vcpInput, uint32(code)); err != nil {
			return Failed("the monitor refused the input %s: %v", r.Arg, err)
		}
	case "power":
		v := uint32(vcpPowerOn)
		if r.Arg == "off" {
			v = vcpPowerOff
		}
		if err := h.SetVCP(vcpPower, v); err != nil {
			if r.Arg == "on" {
				return Failed("the monitor did not take power on (some monitors stop listening to DDC/CI once off; use its power button): %v", err)
			}
			return Failed("the monitor refused power %s: %v", r.Arg, err)
		}
	default:
		return badArgs("monitor set: unknown setting %q", r.Setting)
	}
	return nil
}

// readMonitor reads what the monitor answers. Every value is best effort:
// one a monitor does not answer stays null (unknown for power), and ddc
// says whether it answered anything at all. withCaps also asks for the
// capability string, the source of the input list — the slow part, up to
// a second or two.
func readMonitor(id string, d ddcDisplay, withCaps bool) Monitor {
	m := Monitor{ID: id, Name: sanitizeDevice(d.name), Primary: d.primary, Inputs: []string{}, Power: MonitorPowerUnknown}
	if cur, maxv, err := d.h.GetVCP(vcpBrightness); err == nil {
		m.DDC = true
		if maxv > 0 {
			p := clampPercent(int(math.Round(float64(cur) * 100 / float64(maxv))))
			m.Brightness = &p
		}
	}
	if cur, _, err := d.h.GetVCP(vcpInput); err == nil {
		m.DDC = true
		// The value is a byte; some monitors set bits above it.
		if code := byte(cur & 0xFF); code != 0 {
			name := MonitorInputName(code)
			m.Input = &name
		}
	}
	if cur, _, err := d.h.GetVCP(vcpPower); err == nil {
		m.DDC = true
		m.Power = monitorPowerName(cur)
	}
	if withCaps {
		if caps, err := d.h.Capabilities(); err == nil && strings.TrimSpace(caps) != "" {
			m.DDC = true
			m.Inputs = capabilityInputs(caps)
		}
	}
	if m.Input != nil && !slices.Contains(m.Inputs, *m.Input) && len(m.Inputs) < MaxMonitorInputs {
		m.Inputs = append(m.Inputs, *m.Input)
	}
	return m
}

func monitorPowerName(v uint32) string {
	switch v {
	case 1:
		return MonitorPowerOn
	case 2, 3, 4, 5:
		return MonitorPowerStandby
	}
	return MonitorPowerUnknown
}

// capabilityInputs reads the input source list out of a capability
// string: the values inside "60(…)" in the "vcp(…)" group, e.g.
//
//	(prot(monitor)type(lcd)model(U2720Q)vcp(02 04 10 12 60(0F 11 12) D6(01 04))mccs_ver(2.1))
//
// gives [dp1 hdmi1 hdmi2]. Monitors differ in spacing ("60(0F1112)",
// "60( 0f 11 )"), case and whether the codes are separated at all, so the
// parser reads hex digit runs and splits them into bytes. Anything it
// cannot read gives an empty list.
func capabilityInputs(caps string) []string {
	out := []string{}
	body, ok := capsGroup(caps, "vcp")
	if !ok {
		return out
	}
	values, ok := vcpValues(body, vcpInput)
	if !ok {
		return out
	}
	for _, v := range values {
		if v == 0 {
			continue
		}
		name := MonitorInputName(v)
		if !slices.Contains(out, name) && len(out) < MaxMonitorInputs {
			out = append(out, name)
		}
	}
	return out
}

// capsGroup returns the inside of the first top-level-ish "name(…)" group,
// matching the parentheses.
func capsGroup(caps, name string) (string, bool) {
	lower := strings.ToLower(caps)
	for from := 0; ; {
		i := strings.Index(lower[from:], name+"(")
		if i < 0 {
			return "", false
		}
		i += from
		// "vcp(" must not be the tail of a longer word ("xvcp(").
		if i > 0 && isAlnum(lower[i-1]) {
			from = i + 1
			continue
		}
		start := i + len(name) + 1
		if end, ok := matchParen(lower, start); ok {
			return lower[start:end], true
		}
		return "", false
	}
}

// matchParen finds the ")" closing the group that starts at start.
func matchParen(s string, start int) (int, bool) {
	depth := 1
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// vcpValues finds code in a vcp group and returns the values listed in
// its parentheses.
func vcpValues(body string, code byte) ([]byte, bool) {
	var last []byte // the codes of the hex run just read
	for i := 0; i < len(body); {
		c := body[i]
		switch {
		case isHex(c):
			j := i
			for j < len(body) && isHex(body[j]) {
				j++
			}
			last = hexBytes(body[i:j])
			i = j
		case c == '(':
			end, ok := matchParen(body, i+1)
			if !ok {
				return nil, false
			}
			if len(last) > 0 && last[len(last)-1] == code {
				return hexValues(body[i+1 : end]), true
			}
			last = nil
			i = end + 1
		default:
			if c != ' ' {
				last = nil
			}
			i++
		}
	}
	return nil, false
}

// hexValues reads the values of one group: space-separated bytes, or runs
// of two-digit bytes with no space between them.
func hexValues(s string) []byte {
	var out []byte
	for _, field := range strings.Fields(s) {
		out = append(out, hexBytes(field)...)
	}
	return out
}

// hexBytes splits a run of hex digits into bytes: "0f" → [0x0f], "1" →
// [0x01], "0f1112" → [0x0f 0x11 0x12]. A run of odd length over two digits
// is not something a monitor writes; it gives nothing.
func hexBytes(run string) []byte {
	for _, c := range []byte(run) {
		if !isHex(c) {
			return nil
		}
	}
	switch {
	case len(run) == 0:
		return nil
	case len(run) <= 2:
		n, _ := strconv.ParseUint(run, 16, 8)
		return []byte{byte(n)}
	case len(run)%2 != 0:
		return nil
	}
	out := make([]byte, 0, len(run)/2)
	for i := 0; i < len(run); i += 2 {
		n, _ := strconv.ParseUint(run[i:i+2], 16, 8)
		out = append(out, byte(n))
	}
	return out
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func isAlnum(c byte) bool {
	return isHex(c) || c >= 'g' && c <= 'z' || c >= 'G' && c <= 'Z'
}
