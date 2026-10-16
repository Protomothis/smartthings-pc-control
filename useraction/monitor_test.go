package useraction

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// fakeDDC is a monitor's DDC channel in memory. Nothing in these tests
// reaches dxva2: openDisplays is replaced for every handler test.
type fakeDDC struct {
	vcp     map[byte][2]uint32 // code → {current, maximum}
	caps    string
	capsErr error
	// refuse makes SetVCP fail for these codes.
	refuse map[byte]bool
	sets   []string
}

func (f *fakeDDC) GetVCP(code byte) (uint32, uint32, error) {
	v, ok := f.vcp[code]
	if !ok {
		return 0, 0, errors.New("no reply")
	}
	return v[0], v[1], nil
}

func (f *fakeDDC) SetVCP(code byte, value uint32) error {
	if f.refuse[code] {
		return errors.New("refused")
	}
	f.sets = append(f.sets, fmt.Sprintf("%02X=%d", code, value))
	if f.vcp == nil {
		f.vcp = map[byte][2]uint32{}
	}
	v := f.vcp[code]
	v[0] = value
	f.vcp[code] = v
	return nil
}

func (f *fakeDDC) Capabilities() (string, error) { return f.caps, f.capsErr }

// withFakeDisplays makes the handler see ds; it reports whether they were
// released.
func withFakeDisplays(t *testing.T, ds ...ddcDisplay) *bool {
	t.Helper()
	closed := new(bool)
	saved, savedSettle := openDisplays, ddcSettle
	openDisplays = func() ([]ddcDisplay, func(), error) {
		return append([]ddcDisplay(nil), ds...), func() { *closed = true }, nil
	}
	ddcSettle = 0
	t.Cleanup(func() { openDisplays, ddcSettle = saved, savedSettle })
	return closed
}

const dellCaps = "(prot(monitor)type(LCD)model(U2720Q)cmds(01 02 03 07 0C E3 F3)vcp(02 04 05 08 10 12 14(05 08 0B 0C) 16 18 1A 52 60( 0F 11 12 1B) AA(01 02 04) D6(01 04 05) DC(00 03 05) DF E0 E1 E2(00 1D 22 20 21 0E 12 14 23 24 27) F0(00 0C) FD)mswhql(1)asset_eep(40)mccs_ver(2.1))"

func dellMonitor() *fakeDDC {
	return &fakeDDC{
		vcp:  map[byte][2]uint32{0x10: {30, 75}, 0x60: {0x11, 0x1B}, 0xD6: {1, 5}},
		caps: dellCaps,
	}
}

func TestParseMonitor(t *testing.T) {
	ok := map[string]MonitorRequest{
		"set --id m1 --brightness 40":  {ID: "m1", Setting: "brightness", Level: 40},
		"set --brightness 0 --id m12":  {ID: "m12", Setting: "brightness", Level: 0},
		"set --id m2 --input hdmi1":    {ID: "m2", Setting: "input", Arg: "hdmi1"},
		"set --id m1 --input vcp:0x1b": {ID: "m1", Setting: "input", Arg: "vcp:0x1b"},
		"set --id m1 --power off":      {ID: "m1", Setting: "power", Arg: "off"},
	}
	for line, want := range ok {
		req, err := Parse(append([]string{"monitor"}, strings.Fields(line)...))
		if err != nil || req.Verb != "set" || req.Monitor != want {
			t.Errorf("Parse(monitor %s) = %+v, %v", line, req, err)
		}
	}
	if req, err := Parse([]string{"monitor", "get"}); err != nil || req.Verb != "get" {
		t.Errorf("Parse(monitor get) = %+v, %v", req, err)
	}
	for _, line := range []string{
		"", "get x", "toggle", "set", "set --id m1",
		"set --brightness 40",
		"set --id m0 --brightness 40", "set --id M1 --brightness 40", "set --id m100 --brightness 4",
		"set --id m1 --brightness 101", "set --id m1 --brightness -1", "set --id m1 --brightness +5",
		"set --id m1 --brightness 40 --power on",
		"set --id m1 --input HDMI1", "set --id m1 --input hdmi3", "set --id m1 --input vcp:0x11",
		"set --id m1 --input vcp:0x00", "set --id m1 --input vcp:0x1",
		"set --id m1 --power standby", "set --id m1 --id m2 --power on",
	} {
		args := append([]string{"monitor"}, strings.Fields(line)...)
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) accepted", args)
		}
	}
}

func TestMonitorInputNames(t *testing.T) {
	for code, want := range map[byte]string{0x01: "vga1", 0x03: "dvi1", 0x0F: "dp1", 0x10: "dp2", 0x11: "hdmi1", 0x12: "hdmi2", 0x1B: "vcp:0x1b", 0xFF: "vcp:0xff"} {
		if got := MonitorInputName(code); got != want {
			t.Errorf("MonitorInputName(0x%02X) = %q, want %q", code, got, want)
		}
		if back, ok := MonitorInputCode(want); !ok || back != code {
			t.Errorf("MonitorInputCode(%q) = 0x%02X, %v", want, back, ok)
		}
	}
	if got := MonitorInputLabel("hdmi1"); got != "HDMI1" {
		t.Errorf("label = %q", got)
	}
	if got := MonitorInputLabel("vcp:0x1b"); got != "vcp:0x1b" {
		t.Errorf("label = %q", got)
	}
}

func TestCapabilityInputs(t *testing.T) {
	cases := map[string][]string{
		dellCaps: {"dp1", "hdmi1", "hdmi2", "vcp:0x1b"},
		// No spaces between the codes, lower case, nested groups first.
		"(vcp(02041012(00 01)60(0f1112)d6(0104)))": {"dp1", "hdmi1", "hdmi2"},
		// Single-digit values, the vcp group not first.
		"(prot(monitor)type(lcd)vcp(10 12 60(1 3 11) )mccs_ver(2.2))": {"vga1", "dvi1", "hdmi1"},
		// 160( is not 60(; E60? neither — only a code ending a run counts.
		"(vcp(10 12 D6(01 04)))":         {},
		"(vcp(10 60))":                   {},
		"garbage":                        {},
		"(vcp(10 60(0f 11":               {},
		"(model(x60(11))vcp(10 60(12)))": {"hdmi2"},
	}
	for caps, want := range cases {
		if got := capabilityInputs(caps); !reflect.DeepEqual(got, want) {
			t.Errorf("capabilityInputs(%q) = %q, want %q", caps, got, want)
		}
	}
}

func TestMonitorGetReadsEveryMonitorInIDOrder(t *testing.T) {
	dell := dellMonitor()
	tv := &fakeDDC{} // answers nothing: a TV without DDC/CI
	side := &fakeDDC{vcp: map[byte][2]uint32{0x10: {100, 100}, 0x60: {0x0F, 0}}, capsErr: errors.New("no caps")}
	closed := withFakeDisplays(t,
		ddcDisplay{name: "LG TV", left: 3840, h: tv},
		ddcDisplay{name: "Side", left: -1920, h: side},
		ddcDisplay{name: "DELL U2720Q\x07", primary: true, h: dell},
	)
	_, line, code := runMain(t, "monitor", "get")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, line)
	}
	want := `{"ok":true,"monitors":[` +
		`{"id":"m1","name":"DELL U2720Q","primary":true,"ddc":true,"brightness":40,"input":"hdmi1","inputs":["dp1","hdmi1","hdmi2","vcp:0x1b"],"power":"on"},` +
		`{"id":"m2","name":"Side","primary":false,"ddc":true,"brightness":100,"input":"dp1","inputs":["dp1"],"power":"unknown"},` +
		`{"id":"m3","name":"LG TV","primary":false,"ddc":false,"brightness":null,"input":null,"inputs":[],"power":"unknown"}]}`
	if line != want {
		t.Errorf("got  %s\nwant %s", line, want)
	}
	if !*closed {
		t.Error("the physical monitors were not released")
	}
	if len(dell.sets)+len(tv.sets)+len(side.sets) != 0 {
		t.Error("get wrote to a monitor")
	}
	var reply struct{ Monitors []Monitor }
	if err := json.Unmarshal([]byte(line), &reply); err != nil {
		t.Fatal(err)
	}
	for _, m := range reply.Monitors {
		if err := m.Validate(); err != nil {
			t.Errorf("%s: %v", m.ID, err)
		}
	}
}

func TestMonitorSet(t *testing.T) {
	cases := []struct {
		args []string
		sets []string
		want string
	}{
		{[]string{"--brightness", "40"}, []string{"10=30"},
			`{"ok":true,"monitor":{"id":"m1","name":"DELL","primary":true,"ddc":true,"brightness":40,"input":"hdmi1","inputs":["hdmi1"],"power":"on"}}`},
		{[]string{"--brightness", "100"}, []string{"10=75"}, ""},
		{[]string{"--input", "dp1"}, []string{"60=15"},
			`{"ok":true,"monitor":{"id":"m1","name":"DELL","primary":true,"ddc":true,"brightness":40,"input":"dp1","inputs":["dp1"],"power":"on"}}`},
		{[]string{"--input", "vcp:0x1b"}, []string{"60=27"}, ""},
		{[]string{"--power", "off"}, []string{"D6=4"},
			`{"ok":true,"monitor":{"id":"m1","name":"DELL","primary":true,"ddc":true,"brightness":40,"input":"hdmi1","inputs":["hdmi1"],"power":"standby"}}`},
		{[]string{"--power", "on"}, []string{"D6=1"}, ""},
	}
	for _, tc := range cases {
		dell := dellMonitor()
		withFakeDisplays(t, ddcDisplay{name: "DELL", primary: true, h: dell})
		_, line, code := runMain(t, append([]string{"monitor", "set", "--id", "m1"}, tc.args...)...)
		if code != 0 {
			t.Errorf("%v: exit %d %s", tc.args, code, line)
			continue
		}
		if !reflect.DeepEqual(dell.sets, tc.sets) {
			t.Errorf("%v: wrote %v, want %v", tc.args, dell.sets, tc.sets)
		}
		if tc.want != "" && line != tc.want {
			t.Errorf("%v:\ngot  %s\nwant %s", tc.args, line, tc.want)
		}
	}
}

func TestMonitorSetReportsWhatTheMonitorAcceptedWhenItGoesQuiet(t *testing.T) {
	// A monitor switching input answers nothing for a while.
	quiet := &quietAfterSet{fakeDDC: dellMonitor()}
	withFakeDisplays(t, ddcDisplay{name: "DELL", primary: true, h: quiet})
	m, line, code := runMain(t, "monitor", "set", "--id", "m1", "--input", "dp1")
	if code != 0 {
		t.Fatalf("exit %d %s", code, line)
	}
	mon := m["monitor"].(map[string]any)
	if mon["input"] != "dp1" || mon["ddc"] != true || mon["brightness"] != nil {
		t.Errorf("got %s", line)
	}
}

type quietAfterSet struct {
	*fakeDDC
	set bool
}

func (q *quietAfterSet) SetVCP(code byte, v uint32) error {
	q.set = true
	return q.fakeDDC.SetVCP(code, v)
}

func (q *quietAfterSet) GetVCP(code byte) (uint32, uint32, error) {
	if q.set {
		return 0, 0, errors.New("busy")
	}
	return q.fakeDDC.GetVCP(code)
}

func TestMonitorSetFailures(t *testing.T) {
	dell := dellMonitor()
	dell.refuse = map[byte]bool{0xD6: true, 0x60: true}
	withFakeDisplays(t, ddcDisplay{name: "DELL", primary: true, h: dell}, ddcDisplay{name: "TV", h: &fakeDDC{}})

	for _, tc := range []struct {
		args []string
		code string
		msg  string
	}{
		{[]string{"--id", "m3", "--brightness", "5"}, CodeUnsupported, "m3 not found"},
		{[]string{"--id", "m2", "--brightness", "5"}, CodeUnsupported, "does not answer DDC/CI"},
		{[]string{"--id", "m1", "--input", "dp1"}, CodeFailed, "refused the input dp1"},
		{[]string{"--id", "m1", "--power", "on"}, CodeFailed, "stop listening to DDC/CI once off"},
		{[]string{"--id", "m1", "--power", "off"}, CodeFailed, "refused power off"},
	} {
		m, line, code := runMain(t, append([]string{"monitor", "set"}, tc.args...)...)
		if code != 1 || m["error"] != tc.code || !strings.Contains(m["message"].(string), tc.msg) {
			t.Errorf("%v: exit %d %s", tc.args, code, line)
		}
	}
	if len(dell.sets) != 0 {
		t.Errorf("wrote %v", dell.sets)
	}
}

func TestMonitorEnumerationFailure(t *testing.T) {
	saved := openDisplays
	openDisplays = func() ([]ddcDisplay, func(), error) { return nil, func() {}, errors.New("EnumDisplayMonitors: denied") }
	t.Cleanup(func() { openDisplays = saved })
	m, _, code := runMain(t, "monitor", "get")
	if code != 1 || m["error"] != CodeFailed {
		t.Errorf("exit %d %v", code, m)
	}
}

func TestMonitorGetWithNoMonitors(t *testing.T) {
	withFakeDisplays(t)
	if _, line, code := runMain(t, "monitor", "get"); code != 0 || line != `{"ok":true,"monitors":[]}` {
		t.Errorf("exit %d %s", code, line)
	}
}

func TestMonitorValidate(t *testing.T) {
	b, in := 50, "hdmi1"
	good := Monitor{ID: "m1", Name: "x", Brightness: &b, Input: &in, Inputs: []string{"hdmi1"}, Power: "on"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(*Monitor){
		func(m *Monitor) { m.ID = "x1" },
		func(m *Monitor) { v := 101; m.Brightness = &v },
		func(m *Monitor) { v := "hdmi9"; m.Input = &v },
		func(m *Monitor) { m.Inputs = []string{"HDMI1"} },
		func(m *Monitor) { m.Power = "off" },
		func(m *Monitor) { m.Name = "a\nb" },
	}
	for i, edit := range bad {
		m := good.Clone()
		edit(&m)
		if m.Validate() == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestDisplayConfigLayouts(t *testing.T) {
	// The Windows structs, whose sizes the calls are given or rely on.
	for name, got := range map[string]uintptr{
		"DISPLAYCONFIG_PATH_INFO":          unsafe.Sizeof(pathInfo{}),
		"DISPLAYCONFIG_MODE_INFO":          unsafe.Sizeof(modeInfo{}),
		"DISPLAYCONFIG_SOURCE_DEVICE_NAME": unsafe.Sizeof(sourceDeviceName{}),
		"DISPLAYCONFIG_TARGET_DEVICE_NAME": unsafe.Sizeof(targetDeviceName{}),
		"MONITORINFOEXW":                   unsafe.Sizeof(monitorInfoEx{}),
	} {
		want := map[string]uintptr{
			"DISPLAYCONFIG_PATH_INFO": 72, "DISPLAYCONFIG_MODE_INFO": 64,
			"DISPLAYCONFIG_SOURCE_DEVICE_NAME": 84, "DISPLAYCONFIG_TARGET_DEVICE_NAME": 420,
			"MONITORINFOEXW": 104,
		}[name]
		if got != want {
			t.Errorf("sizeof(%s) = %d, want %d", name, got, want)
		}
	}
	if want := unsafe.Sizeof(uintptr(0)) + 256; unsafe.Sizeof(physicalMonitor{}) != want {
		t.Errorf("sizeof(PHYSICAL_MONITOR) = %d, want %d", unsafe.Sizeof(physicalMonitor{}), want)
	}
}
