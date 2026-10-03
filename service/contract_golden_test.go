package service

// Contract tests for /st/v1 and /api/config against the shared golden files
// in testdata/st-v1 (#125, docs/design/refactor-plan.md §3.1).
//
// The Edge driver's Lua tests (edge/tests/contract_test.lua) read the very
// same files, so a field renamed on either side fails one of the two suites:
//
//   - status.*.json and push.*.json are written by this file from the real
//     handlers; the Lua side feeds them through state.apply_status /
//     features.remember / push.apply and asserts the rows with literals.
//   - command.*.json carry a hand-written "request" (what the driver sends)
//     and a generated "response". The Go side decodes the request strictly
//     with the handler's own request type and runs it through the handler;
//     the Lua side runs the real capability handler and compares the body it
//     sends with "request".
//
// Regenerate after an intended API change (testdata/st-v1/README.md):
//
//	go test ./service -run TestContract -update
//
// Every value here is fixed (clock, ids, adapters, session) through the
// existing test seams. The few that have no seam are listed as volatile per
// fixture: the key must be present with the right JSON type, and only its
// value is replaced by a fixed placeholder before the comparison.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/internal/config"
	"github.com/Protomothis/smartthings-pc-control/service/stapi"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

var updateGolden = flag.Bool("update", false, "rewrite the contract fixtures under testdata/st-v1")

// goldenDir is testdata/st-v1 at the repository root; go test runs in service/.
var goldenDir = filepath.Join("..", "testdata", "st-v1")

// The golden clock: every time the fixtures carry is this one or derived
// from it, in a fixed zone so the files do not depend on the test machine.
var (
	goldenZone = time.FixedZone("KST", 9*60*60)
	goldenNow  = time.Date(2026, 10, 1, 21, 0, 0, 0, goldenZone)
)

const (
	goldenSecret    = "golden-secret"
	goldenMachineID = "4c4c4544-0042-3510-8052-b4c04f4a3732"
	goldenHub       = "192.168.1.20"
)

// ---- golden files ------------------------------------------------------------

// encodeGolden is the on-disk form: two-space indent, keys sorted (they are
// decoded maps), no HTML escaping, trailing newline.
func encodeGolden(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encode golden: %v", err)
	}
	return buf.Bytes()
}

func decodeAny(t *testing.T, raw []byte, what string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", what, err, raw)
	}
	return v
}

func readGolden(t *testing.T, name string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to create it)", name, err)
	}
	return decodeAny(t, raw, name)
}

func writeGolden(t *testing.T, name string, v any) {
	t.Helper()
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goldenDir, name), encodeGolden(t, v), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", name)
}

// compareGolden checks got against the file, or rewrites the file with -update.
func compareGolden(t *testing.T, name string, got any) {
	t.Helper()
	if *updateGolden {
		writeGolden(t, name, got)
		return
	}
	want := readGolden(t, name)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s does not match the handler output (an API change? see testdata/st-v1/README.md):\n%s",
			name, goldenDiff(want, got))
	}
}

// goldenDiff lists the JSON paths whose values differ, at most a dozen.
func goldenDiff(want, got any) string {
	var lines []string
	var walk func(path string, w, g any)
	walk = func(path string, w, g any) {
		if reflect.DeepEqual(w, g) {
			return
		}
		wm, wok := w.(map[string]any)
		gm, gok := g.(map[string]any)
		if wok && gok {
			keys := make([]string, 0, len(wm)+len(gm))
			for k := range wm {
				keys = append(keys, k)
			}
			for k := range gm {
				if _, dup := wm[k]; !dup {
					keys = append(keys, k)
				}
			}
			slices.Sort(keys)
			for _, k := range keys {
				wv, inW := wm[k]
				gv, inG := gm[k]
				switch {
				case !inW:
					lines = append(lines, fmt.Sprintf("  %s%s: not in the golden, handler has %s", path, k, compactJSON(gv)))
				case !inG:
					lines = append(lines, fmt.Sprintf("  %s%s: golden has %s, the handler does not send it", path, k, compactJSON(wv)))
				default:
					walk(path+k+".", wv, gv)
				}
			}
			return
		}
		wa, wok := w.([]any)
		ga, gok := g.([]any)
		if wok && gok && len(wa) == len(ga) {
			for i := range wa {
				walk(fmt.Sprintf("%s[%d].", strings.TrimSuffix(path, "."), i), wa[i], ga[i])
			}
			return
		}
		lines = append(lines, fmt.Sprintf("  %s: golden %s, handler %s", strings.TrimSuffix(path, "."), compactJSON(w), compactJSON(g)))
	}
	walk("", want, got)
	if len(lines) > 12 {
		lines = append(lines[:12], "  ...")
	}
	return strings.Join(lines, "\n")
}

func compactJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if len(raw) > 120 {
		return string(raw[:117]) + "..."
	}
	return string(raw)
}

// ---- volatile fields ---------------------------------------------------------

// volatileField is a value no existing seam can pin (the machine's name, its
// uptime, a timer started from the wall clock). The key still has to be in
// the output with the expected type — a renamed or dropped field fails here
// rather than being papered over — and only the value is replaced.
type volatileField struct {
	path  string // dotted object path
	kind  string // "string", "number", "time" (RFC3339) or "clock" (15:04:05)
	value any    // the placeholder written to the golden file
}

func vf(path, kind string, value any) volatileField {
	return volatileField{path: path, kind: kind, value: value}
}

// statusVolatile are the volatile fields of a status document at prefix
// ("" for /st/v1/status, "status." inside a push body).
func statusVolatile(prefix string, scheduleActive bool) []volatileField {
	out := []volatileField{
		// os.Hostname and windows.DurationSinceBoot: no seam.
		vf(prefix+"hostname", "string", "GOLDEN-PC"),
		vf(prefix+"uptime_seconds", "number", float64(93784)),
	}
	if scheduleActive {
		out = append(out, scheduleVolatile(prefix+"schedule.")...)
	}
	return out
}

// scheduleVolatile: setSchedule arms a real timer from time.Now, and
// getSchedule counts down with time.Until.
func scheduleVolatile(prefix string) []volatileField {
	return []volatileField{
		vf(prefix+"remaining_seconds", "number", float64(1800)),
		vf(prefix+"execute_at", "time", "2026-10-01T21:30:00+09:00"),
	}
}

func normalize(t *testing.T, doc any, fields ...volatileField) any {
	t.Helper()
	for _, f := range fields {
		keys := strings.Split(f.path, ".")
		obj, ok := doc.(map[string]any)
		for _, k := range keys[:len(keys)-1] {
			if !ok {
				break
			}
			obj, ok = obj[k].(map[string]any)
		}
		if !ok {
			t.Fatalf("volatile field %s: its parent object is missing (renamed or dropped?)", f.path)
		}
		last := keys[len(keys)-1]
		v, present := obj[last]
		if !present {
			t.Fatalf("volatile field %s is missing (renamed or dropped?)", f.path)
		}
		switch f.kind {
		case "string":
			if s, _ := v.(string); s == "" {
				t.Fatalf("%s = %v, want a non-empty string", f.path, v)
			}
		case "number":
			if _, isNum := v.(float64); !isNum {
				t.Fatalf("%s = %v, want a number", f.path, v)
			}
		case "time":
			s, _ := v.(string)
			if _, err := time.Parse(time.RFC3339, s); err != nil {
				t.Fatalf("%s = %v, want RFC3339", f.path, v)
			}
		case "clock":
			s, _ := v.(string)
			if _, err := time.Parse("15:04:05", s); err != nil {
				t.Fatalf("%s = %v, want 15:04:05", f.path, v)
			}
		default:
			t.Fatalf("volatile field %s: unknown kind %q", f.path, f.kind)
		}
		obj[last] = f.value
	}
	return doc
}

// ---- the golden world --------------------------------------------------------

type worldOpts struct {
	// schedule starts a 30-minute SmartThings shutdown schedule.
	schedule bool
	// locked is what the (stubbed) WTS query says about the session.
	locked bool
}

// goldenConfig turns every v1.2.0 block on. ShutdownGrace is on so the grace
// block says so; the command fixtures turn it off (goldenCommandConfig).
func goldenConfig() Config {
	return Config{
		Port:          5001,
		Secret:        goldenSecret,
		ShutdownGrace: true,
		GraceSeconds:  120,
		SmartThings:   SmartThingsConfig{ExposeSession: true, ExposeSessionUser: true},
		Awake:         AwakeConfig{DefaultMinutes: 60},
		Activity:      goldenActivityConfig(),
		Media:         MediaConfig{Enabled: true, NowPlaying: true},
		NotifyPC:      NotifyPCConfig{Enabled: true},
		Presets: []Preset{
			{Slot: 3, Name: "게임 모드", Type: "program", Path: `C:\Games\Steam\steam.exe`, Args: []string{"-bigpicture"}},
			{Slot: 1, Name: "대시보드", Type: "url", Path: "https://example.com/dashboard"},
		},
	}
}

// ---- activity (#123: watch slots 1–5 on the PC device, slot = priority).
// Everything the fixtures say about it comes from these two functions and
// the activity.changed push case; update them together with the Lua
// section "activity (#123)" in edge/tests/contract_test.lua.

// goldenActivityConfig fills slots 1 and 3, leaving 2, 4 and 5 empty.
func goldenActivityConfig() ActivityConfig {
	return ActivityConfig{Enabled: true, Watch: []ActivityWatch{
		{Slot: 1, Process: "steam.exe", Label: "Steam"},
		{Slot: 3, Process: "obs64.exe", Label: "OBS"},
	}}
}

// goldenActivity makes the scanner see Steam running (OBS listed, not
// running): apps [1 steam running, 3 obs stopped], top steam.exe.
func goldenActivity(t *testing.T, cfg Config) {
	t.Helper()
	stubProcesses(t, "explorer.exe", "steam.exe")
	activityScan.Scan(cfg.Activity)
}

// ---- end of the activity section.

var goldenTrack = useraction.NowPlaying{Status: "playing", Title: "Hype Boy", Artist: "NewJeans", Album: "New Jeans", App: "Spotify"}

const goldenSpeaker = "스피커 (Realtek(R) Audio)"

// goldenWorld puts every piece of state the status document reads into a
// known value, through the seams the feature tests already use, and
// restores it afterwards. It returns the keep-awake fake.
func goldenWorld(t *testing.T, cfg Config, opts worldOpts) *fakeAwake {
	t.Helper()
	stPushSetup(t, cfg)

	// service_version and update: the build stamp and the checker cache.
	savedVersion := Version
	Version = "v1.2.0"
	savedLatest := latestReleaseTag()
	noteLatestRelease("v1.2.1")
	t.Cleanup(func() {
		Version = savedVersion
		latestRelease.Store(savedLatest)
	})

	// machine_id: read once per process from the registry. Force the real
	// read first so restoring leaves the next caller a real value.
	machineID()
	savedID := machineIDValue
	machineIDValue = goldenMachineID
	t.Cleanup(func() { machineIDValue = savedID })

	savedClean := lastShutdownClean.Load()
	lastShutdownClean.Store(true)
	t.Cleanup(func() { lastShutdownClean.Store(savedClean) })

	// wol: two adapters; the hub reached us on the Ethernet one (#96 rule ①).
	stubWoL(t, WoLStatus{Ready: true, Adapters: []WoLAdapter{
		{Name: "이더넷", MacAddress: "B4-2E-99-45-B4-F5", IPs: []string{"192.168.1.10", "fe80::1"}, Status: "Up", WoLEnabled: true, WoLCapable: true},
		{Name: "Wi-Fi", MacAddress: "3C-A9-F4-11-22-33", IPs: []string{"192.168.1.11"}, Status: "Up", WoLEnabled: false, WoLCapable: true},
	}})
	stSrv.ResetHubLocalIP()
	stSrv.NoteHubLocalIP(net.ParseIP("192.168.1.10"))
	t.Cleanup(stSrv.ResetHubLocalIP)

	savedDisplay := getDisplayState()
	setDisplayState("on")
	t.Cleanup(func() { setDisplayState(savedDisplay) })

	// session: WTS through the seam, idle through the tray heartbeat.
	savedQuery := sources.sessionQuery
	sources.sessionQuery = func() (sessionInfo, error) { return sessionInfo{Locked: opts.locked, User: "golden"}, nil }
	clock.idle = func() time.Time { return goldenNow }
	noteIdleHeartbeat(754)
	t.Cleanup(func() {
		sources.sessionQuery = savedQuery
		clock.idle = time.Now
		resetIdleHeartbeat()
	})

	// awake: on for 90 minutes from the golden clock.
	fa := stubAwake(t)
	fa.mu.Lock()
	fa.now = goldenNow
	fa.mu.Unlock()
	if _, err := fa.ctl.TurnOn(90); err != nil {
		t.Fatal(err)
	}

	// battery: a charging laptop at 76%.
	stubBattery(t, &batteryMonitor{
		Last:  batteryInfo{Present: true, Percent: 76, Charging: true, AC: true},
		Known: true,
		Read: func() (systemPowerStatus, error) {
			return systemPowerStatus{}, fmt.Errorf("not read in the golden world")
		},
		OnChange: emitBatteryChanged,
	})

	goldenActivity(t, cfg)

	// audio and media: one reading each, fresh against the golden clock.
	resetAudioSample()
	resetMediaSample()
	savedPresent := sys.sessionPresent
	sys.sessionPresent = func() bool { return true }
	clock.audio = func() time.Time { return goldenNow }
	t.Cleanup(func() {
		resetAudioSample()
		resetMediaSample()
		clock.audio = time.Now
		sys.sessionPresent = savedPresent
	})
	noteAudioSample(useraction.Audio{Volume: 35, Muted: false, Device: goldenSpeaker}, goldenNow.Add(-30*time.Second))
	noteMediaSampleChange(goldenTrack, goldenNow.Add(-20*time.Second))

	// last_command: a preset run from SmartThings five minutes ago.
	lastRemoteMu.Lock()
	savedRemote := lastRemote
	lastRemote = remoteRecord{Command: "preset", From: goldenHub, Origin: "smartthings", At: goldenNow.Add(-5 * time.Minute),
		Preset: &presetRecord{Slot: 3, Name: "게임 모드", Result: "started"}}
	lastRemoteMu.Unlock()
	t.Cleanup(func() {
		lastRemoteMu.Lock()
		lastRemote = savedRemote
		lastRemoteMu.Unlock()
	})

	if opts.schedule {
		// The stub is the safety net: the timer is cancelled at cleanup,
		// but nothing in a test may ever shut the machine down.
		stubCommand(t, "shutdown")
		if err := setSchedule("shutdown", 30*time.Minute, originSmartThings); err != nil {
			t.Fatal(err)
		}
	}
	return fa
}

func goldenStatus(t *testing.T) any {
	t.Helper()
	w := stDo(t, "GET", "/st/v1/status", goldenHub, goldenSecret, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d (%s)", w.Code, w.Body.String())
	}
	return decodeAny(t, w.Body.Bytes(), "status")
}

// ---- status ------------------------------------------------------------------

func TestContractStatusFull(t *testing.T) {
	goldenWorld(t, goldenConfig(), worldOpts{schedule: true})
	got := normalize(t, goldenStatus(t), statusVolatile("", true)...)
	compareGolden(t, "status.full.json", got)
}

// TestContractStatusMinimalStillServed keeps the v1.0-era contract: every key
// status.minimal-1.0.json has (the shape service v1.1.x sent to edge driver
// 1.0.x, which stays installed on hubs) must still be in today's status with
// the same JSON type. The fixture is hand-written from
// `git show v1.1.2:service/st_api.go` and is never regenerated.
func TestContractStatusMinimalStillServed(t *testing.T) {
	goldenWorld(t, goldenConfig(), worldOpts{schedule: true})
	old := readGolden(t, "status.minimal-1.0.json")
	if m, _ := old.(map[string]any); m["features"] != nil {
		t.Fatal("status.minimal-1.0.json grew a features key; it must stay the pre-v1.2.0 shape")
	}
	assertShapeKept(t, "status", old, goldenStatus(t))
}

// offWorld is a fresh install on a desktop: the defaults for every v1.2.0
// option (config.Default plus a secret), nothing sampled yet, keep-awake
// off, no schedule, no remote command so far, the screen turned off and the
// one adapter without WoL. Steam is running but not on any watch list.
func offWorld(t *testing.T) {
	t.Helper()
	cfg := config.Default()
	cfg.Secret = goldenSecret
	stSetup(t, cfg)

	savedVersion, savedLatest := Version, latestReleaseTag()
	Version = "v1.2.0"
	latestRelease.Store("")
	machineID()
	savedID := machineIDValue
	machineIDValue = goldenMachineID
	savedClean := lastShutdownClean.Load()
	lastShutdownClean.Store(false)
	t.Cleanup(func() {
		Version = savedVersion
		latestRelease.Store(savedLatest)
		machineIDValue = savedID
		lastShutdownClean.Store(savedClean)
	})

	stubWoL(t, WoLStatus{Adapters: []WoLAdapter{
		{Name: "이더넷", MacAddress: "B4-2E-99-45-B4-F5", IPs: []string{"192.168.1.10"}, Status: "Up", WoLCapable: true},
	}})
	stSrv.ResetHubLocalIP()

	savedDisplay := getDisplayState()
	setDisplayState("off")
	t.Cleanup(func() { setDisplayState(savedDisplay) })

	stubAwake(t)
	stubBattery(t, &batteryMonitor{
		Last:  batteryInfo{Present: false, Percent: -1, AC: true},
		Known: true,
		Read: func() (systemPowerStatus, error) {
			return systemPowerStatus{}, fmt.Errorf("not read in the off world")
		},
	})
	stubProcesses(t, "explorer.exe", "steam.exe")
	activityScan.Scan(cfg.Activity)

	resetAudioSample()
	resetMediaSample()
	resetIdleHeartbeat()
	savedPresent := sys.sessionPresent
	sys.sessionPresent = func() bool { return true }
	t.Cleanup(func() { sys.sessionPresent = savedPresent })

	lastRemoteMu.Lock()
	savedRemote := lastRemote
	lastRemote = remoteRecord{}
	lastRemoteMu.Unlock()
	t.Cleanup(func() {
		lastRemoteMu.Lock()
		lastRemote = savedRemote
		lastRemoteMu.Unlock()
	})
}

// TestContractStatusOff pins the other end from status.full.json: what a
// driver sees from a PC where every option is still at its default. Each
// block is present in its "off" form (activity disabled with no apps, audio
// unavailable, media none, awake off, the desktop battery, the session
// block reduced to exposed:false), features lists what the defaults turn
// on (no battery, activity or nowplaying), and nothing the user did not opt
// into leaks (no user, no idle time, no process name).
func TestContractStatusOff(t *testing.T) {
	offWorld(t)
	w := stDo(t, "GET", "/st/v1/status", goldenHub, goldenSecret, "")
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type = %q", ct)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d (%s)", w.Code, w.Body.String())
	}
	got := normalize(t, decodeAny(t, w.Body.Bytes(), "status"), statusVolatile("", false)...)
	compareGolden(t, "status.off.json", got)
}

func jsonKindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

// assertShapeKept fails for every key of old missing from now or carrying
// another JSON type. null in old accepts anything (it was optional then).
func assertShapeKept(t *testing.T, path string, old, now any) {
	t.Helper()
	if old == nil {
		return
	}
	if jsonKindOf(old) != jsonKindOf(now) {
		t.Errorf("%s: was %s in v1.0, is %s now", path, jsonKindOf(old), jsonKindOf(now))
		return
	}
	switch o := old.(type) {
	case map[string]any:
		n := now.(map[string]any)
		for k, v := range o {
			nv, ok := n[k]
			if !ok {
				t.Errorf("%s.%s: served to v1.0 drivers, missing now", path, k)
				continue
			}
			assertShapeKept(t, path+"."+k, v, nv)
		}
	case []any:
		n := now.([]any)
		if len(o) > 0 && len(n) > 0 {
			assertShapeKept(t, path+"[0]", o[0], n[0])
		}
	}
}

// ---- commands ----------------------------------------------------------------

// commandFixture is one command.*.json file.
type commandFixture struct {
	Doc      string          `json:"_doc"`
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	Request  json.RawMessage `json:"request"`
	Response struct {
		Code int `json:"code"`
		Body any `json:"body"`
	} `json:"response"`
}

// goldenCommandConfig is goldenConfig without the grace period, so a power
// command is executed (by a stub) instead of becoming a schedule.
func goldenCommandConfig() Config {
	cfg := goldenConfig()
	cfg.ShutdownGrace = false
	return cfg
}

type commandCase struct {
	// setup runs after the golden world, before the request.
	setup    func(t *testing.T)
	volatile []volatileField
}

func withConfig(t *testing.T, edit func(*Config)) {
	t.Helper()
	cfg := getConfig()
	edit(&cfg)
	setConfig(cfg)
}

// commandCases is keyed by fixture name. Every command.*.json needs a case
// here and every case a file.
var commandCases = map[string]commandCase{
	"command.shutdown.json": {setup: func(t *testing.T) { stubCommand(t, "shutdown") }},
	"command.schedule.json": {
		setup:    func(t *testing.T) { stubCommand(t, "restart") },
		volatile: scheduleVolatile("schedule."),
	},
	"command.cancel.json": {setup: func(t *testing.T) {
		stubCommand(t, "restart")
		if err := setSchedule("restart", 30*time.Minute, originSmartThings); err != nil {
			t.Fatal(err)
		}
	}},
	"command.volume.json": {setup: func(t *testing.T) {
		stubMediaRun(t, UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 30, Muted: false, Device: goldenSpeaker}}, nil)
	}},
	"command.volume.no-user.json": {setup: func(t *testing.T) {
		stubMediaRun(t, UserActionResult{}, fmt.Errorf("get session: %w", errNoUserSession))
	}},
	"command.mute.json": {setup: func(t *testing.T) {
		stubMediaRun(t, UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 35, Muted: true, Device: goldenSpeaker}}, nil)
	}},
	"command.unmute.json": {setup: func(t *testing.T) {
		stubMediaRun(t, UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 35, Muted: false, Device: goldenSpeaker}}, nil)
	}},
	"command.next.json": {setup: func(t *testing.T) { stubMediaRun(t, UserActionResult{OK: true}, nil) }},
	"command.next.media-disabled.json": {setup: func(t *testing.T) {
		stubMediaRun(t, UserActionResult{OK: true}, nil)
		withConfig(t, func(c *Config) { c.Media.Enabled = false })
	}},
	"command.preset.json":   {setup: func(t *testing.T) { fakePresetRun(t, `{"ok":true,"started":true}`, nil) }},
	"command.awake.json":    {},
	"command.awakeoff.json": {},
	"command.notify.json":   {setup: func(t *testing.T) { fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil) }},
	"command.notify.disabled.json": {setup: func(t *testing.T) {
		fakeNotifyRun(t, `{"ok":true,"toast":"shown"}`, nil)
		withConfig(t, func(c *Config) { c.NotifyPC.Enabled = false })
	}},
	"command.subscribe.json": {
		setup: func(t *testing.T) {
			// Ids count up for the life of the process; start this one at 1.
			stSrv.RestartSubscriptionIDs()
		},
		volatile: []volatileField{vf("expires_at", "time", "2026-10-01T21:10:00+09:00")},
	},
}

// decodeRequestStrictly decodes a fixture request with the type the handler
// for path decodes into, refusing unknown keys: a key the driver sends that
// the service no longer reads (a renamed json tag) fails here.
func decodeRequestStrictly(t *testing.T, method, path string, raw json.RawMessage) {
	t.Helper()
	var target any
	switch {
	case method == "POST" && path == "/st/v1/command":
		target = &stapi.CommandRequest{}
	case method == "POST" && path == "/st/v1/notify":
		target = &stapi.NotifyRequest{}
	case method == "POST" && path == "/st/v1/subscribe":
		target = &stapi.SubscribeRequest{}
	case method == "DELETE":
		if s := strings.TrimSpace(string(raw)); s != "" && s != "null" {
			t.Fatalf("%s %s carries no body, the fixture has %s", method, path, s)
		}
		return
	default:
		t.Fatalf("no request type known for %s %s", method, path)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		var compact bytes.Buffer
		_ = json.Compact(&compact, raw)
		t.Fatalf("the service does not decode the driver's request %s: %v", compact.String(), err)
	}
}

func goldenFiles(t *testing.T, pattern string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(goldenDir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, filepath.Base(p))
	}
	slices.Sort(names)
	return names
}

func TestContractCommands(t *testing.T) {
	files := goldenFiles(t, "command.*.json")
	for _, name := range files {
		if _, ok := commandCases[name]; !ok {
			t.Errorf("%s has no case in commandCases", name)
		}
	}
	for name := range commandCases {
		if !slices.Contains(files, name) {
			t.Errorf("commandCases[%q] has no fixture file (write its request by hand first)", name)
		}
	}

	for _, name := range files {
		tc, ok := commandCases[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(goldenDir, name))
			if err != nil {
				t.Fatal(err)
			}
			var fx commandFixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			goldenWorld(t, goldenCommandConfig(), worldOpts{})
			if tc.setup != nil {
				tc.setup(t)
			}

			decodeRequestStrictly(t, fx.Method, fx.Path, fx.Request)
			body := ""
			if s := strings.TrimSpace(string(fx.Request)); s != "" && s != "null" {
				body = s
			}
			w := stDo(t, fx.Method, fx.Path, goldenHub, goldenSecret, body)
			// A plain command.<name>.json is a request the service must
			// accept; command.<name>.<error>.json documents a refusal.
			if strings.Count(name, ".") == 2 && w.Code != http.StatusOK {
				t.Fatalf("the service refused the driver's request: %d %s", w.Code, w.Body.String())
			}
			got := normalize(t, decodeAny(t, w.Body.Bytes(), "response"), tc.volatile...)

			if *updateGolden {
				var doc map[string]any
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				doc["response"] = map[string]any{"code": w.Code, "body": got}
				writeGolden(t, name, doc)
				return
			}
			if w.Code != fx.Response.Code {
				t.Errorf("code %d, golden says %d (%s)", w.Code, fx.Response.Code, w.Body.String())
			}
			if !reflect.DeepEqual(got, fx.Response.Body) {
				t.Errorf("%s response does not match:\n%s", name,
					goldenDiff(fx.Response.Body, got))
			}
		})
	}
}

// ---- pushes ------------------------------------------------------------------

type pushCase struct {
	opts    worldOpts
	trigger func(t *testing.T, fa *fakeAwake)
	// volatile on top of "at" and the status ones.
	volatile []volatileField
}

// pushCases is keyed by fixture name: push.<type>.json. Each trigger calls
// the real emitter the service uses for that event.
var pushCases = map[string]pushCase{
	"push.audio.changed.json": {trigger: func(t *testing.T, _ *fakeAwake) {
		recordAudioSample(useraction.Audio{Volume: 50, Muted: true, Device: goldenSpeaker}, goldenNow)
	}},
	"push.media.changed.json": {trigger: func(t *testing.T, _ *fakeAwake) {
		paused := goldenTrack
		paused.Status = "paused"
		recordMediaSample(paused, goldenNow)
	}},
	// activity (#123): OBS starts next to Steam. Both run, top stays
	// steam.exe (slot 1 ranks above slot 3); data = status.activity.
	"push.activity.changed.json": {trigger: func(t *testing.T, _ *fakeAwake) {
		sys.processes = func() ([]string, error) { return []string{"explorer.exe", "steam.exe", "obs64.exe"}, nil }
		activityTick(getConfig().Activity)
	}},
	"push.awake.changed.json": {trigger: func(t *testing.T, fa *fakeAwake) {
		// The service's controller reports through emitAwakeChanged.
		fa.ctl.Hooks.OnChange = emitAwakeChanged
		if _, _, err := fa.ctl.TurnOff(); err != nil {
			t.Fatal(err)
		}
	}},
	"push.battery.changed.json": {trigger: func(t *testing.T, _ *fakeAwake) {
		// Unplugged at 75%: BatteryFlag 1 (high), AC offline.
		battery.Read = func() (systemPowerStatus, error) {
			return systemPowerStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 75}, nil
		}
		if !battery.Poll() {
			t.Fatal("the battery reading did not change")
		}
	}},
	"push.display.changed.json": {trigger: func(t *testing.T, _ *fakeAwake) { setDisplayState("off") }},
	"push.session.locked.json": {
		opts:    worldOpts{locked: true},
		trigger: func(t *testing.T, _ *fakeAwake) { emitSessionLock(sessionInfo{Locked: true, User: "golden"}) },
	},
	"push.schedule.created.json": {
		trigger: func(t *testing.T, _ *fakeAwake) {
			stubCommand(t, "restart")
			if err := setSchedule("restart", 30*time.Minute, originSmartThings); err != nil {
				t.Fatal(err)
			}
		},
		volatile: append(scheduleVolatile("status.schedule."), vf("data.execute_at", "clock", "21:30:00")),
	},
	"push.schedule.cancelled.json": {
		opts:    worldOpts{schedule: true},
		trigger: func(t *testing.T, _ *fakeAwake) { cancelScheduleBy("smartthings") },
	},
	"push.power.stopping.json": {trigger: func(t *testing.T, _ *fakeAwake) {
		// The suspend broadcast; no power command ran, so the reason is
		// the broadcast's own.
		(&powerTracker{now: func() time.Time { return goldenNow }}).handle(pbtAPMSuspend)
	}},
}

func TestContractPushes(t *testing.T) {
	files := goldenFiles(t, "push.*.json")
	for _, name := range files {
		if _, ok := pushCases[name]; !ok {
			t.Errorf("%s has no case in pushCases", name)
		}
	}
	names := make([]string, 0, len(pushCases))
	for name := range pushCases {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		tc := pushCases[name]
		t.Run(name, func(t *testing.T) {
			if !*updateGolden && !slices.Contains(files, name) {
				t.Fatalf("%s is missing (run with -update)", name)
			}
			fa := goldenWorld(t, goldenConfig(), tc.opts)
			startNotifier(nil)
			t.Cleanup(stopNotifier)
			cb := newCallbackServer(t)
			subscribeTo(t, cb, 600)

			tc.trigger(t, fa)
			body := cb.wait(t)

			wantType := strings.TrimSuffix(strings.TrimPrefix(name, "push."), ".json")
			if body["type"] != wantType {
				t.Fatalf("pushed %v, want %s", body["type"], wantType)
			}
			fields := append([]volatileField{vf("at", "time", "2026-10-01T21:00:05+09:00")},
				statusVolatile("status.", false)...)
			got := normalize(t, any(body), append(fields, tc.volatile...)...)
			compareGolden(t, name, got)
		})
	}
}

// ---- /api/config -------------------------------------------------------------

// TestContractConfigMasked pins GET /api/config: the desktop app's view of
// config.json, with the Telegram bot token masked ("****" + last four) and
// bot_token_set beside it. It is read with the desktop app's local trusted
// session (C5): without one the presets' path and args are masked
// (webui_localonly_test.go).
func TestContractConfigMasked(t *testing.T) {
	cfg := goldenConfig()
	cfg.Telegram = TelegramConfig{
		Enabled: true, BotToken: "123456789:AAgoldenTokenValueEndsIn9876", ChatID: "123456789",
		Detail: "simple", Lang: "ko",
	}
	withLiveConfig(t, cfg)

	r := asLocalApp(t, httptest.NewRequest("GET", "/api/config", nil))
	w := httptest.NewRecorder()
	webAPI(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/config: %d (%s)", w.Code, w.Body.String())
	}
	raw, _ := io.ReadAll(w.Body)
	if bytes.Contains(raw, []byte("AAgoldenTokenValue")) {
		t.Fatalf("the bot token leaked unmasked: %s", raw)
	}
	compareGolden(t, "api-config.get.json", decodeAny(t, raw, "/api/config"))
}
