package privacy

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// fakeHive is a consent store in memory: path → [start, stop]. A path with
// children but no times is a plain key.
type fakeHive struct {
	times  map[string][2]uint64
	keys   map[string][]string
	broken map[string]bool // SubKeys fails with another error
	closed *int
}

func newHive() *fakeHive {
	return &fakeHive{times: map[string][2]uint64{}, keys: map[string][]string{}, broken: map[string]bool{}}
}

// app adds an entry under cap ("webcam"/"microphone"); nonPackaged puts it
// under NonPackaged.
func (h *fakeHive) app(cap, name string, nonPkg bool, start, stop uint64) *fakeHive {
	base := storeRoot + cap
	if _, ok := h.keys[base]; !ok {
		h.keys[base] = nil
	}
	parent := base
	if nonPkg {
		parent = base + `\NonPackaged`
		if !slices.Contains(h.keys[base], "NonPackaged") {
			h.keys[base] = append(h.keys[base], "NonPackaged")
		}
	}
	h.keys[parent] = append(h.keys[parent], name)
	h.times[parent+`\`+name] = [2]uint64{start, stop}
	return h
}

func (h *fakeHive) SubKeys(path string) ([]string, error) {
	if h.broken[path] {
		return nil, errors.New("access denied")
	}
	k, ok := h.keys[path]
	if !ok {
		return nil, ErrNotFound
	}
	return k, nil
}

func (h *fakeHive) Times(path string) (uint64, uint64, error) {
	t, ok := h.times[path]
	if !ok {
		return 0, 0, ErrNotFound
	}
	return t[0], t[1], nil
}

func (h *fakeHive) Close() error {
	if h.closed != nil {
		*h.closed++
	}
	return nil
}

// ft is the FILETIME of t.
func ft(t time.Time) uint64 { return uint64(t.UnixNano()/100) + fileTimeEpoch }

var (
	zone = time.FixedZone("KST", 9*60*60)
	t0   = time.Date(2026, 10, 1, 20, 0, 0, 0, zone)
)

func TestReadStates(t *testing.T) {
	cases := []struct {
		name      string
		hive      *fakeHive
		cam, mic  string
		since     time.Time
		inUseDevs []string
	}{
		{"nothing ever used", newHive(), Idle, Idle, time.Time{}, nil},
		{
			"packaged camera app running",
			newHive().app("webcam", "Microsoft.WindowsCamera_8wekyb3d8bbwe", false, ft(t0), 0).
				app("microphone", "Microsoft.WindowsCamera_8wekyb3d8bbwe", false, ft(t0.Add(-time.Hour)), ft(t0.Add(-50*time.Minute))),
			InUse, Idle, t0, []string{Camera},
		},
		{
			"desktop app on both, earliest start wins",
			newHive().app("webcam", `C:#Program Files#Zoom#Zoom.exe`, true, ft(t0.Add(5*time.Minute)), 0).
				app("microphone", `C:#Program Files#Zoom#Zoom.exe`, true, ft(t0), 0),
			InUse, InUse, t0, []string{Camera, Microphone},
		},
		{
			"all stopped: latest stop",
			newHive().app("webcam", "a", false, ft(t0), ft(t0.Add(time.Minute))).
				app("webcam", "b", true, ft(t0), ft(t0.Add(3*time.Minute))).
				app("microphone", "c", false, ft(t0), ft(t0.Add(2*time.Minute))),
			Idle, Idle, t0.Add(3 * time.Minute), nil,
		},
		{
			"never started (start 0, stop 0) is not in use",
			newHive().app("webcam", "a", false, 0, 0),
			Idle, Idle, time.Time{}, nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Read(c.hive)
			if r.Camera != c.cam || r.Microphone != c.mic {
				t.Fatalf("got %s / %s, want %s / %s", r.Camera, r.Microphone, c.cam, c.mic)
			}
			if !r.Since.Equal(c.since) {
				t.Errorf("since %v, want %v", r.Since, c.since)
			}
			if got := r.InUseDevices(); !slices.Equal(got, c.inUseDevs) {
				t.Errorf("in use %v, want %v", got, c.inUseDevs)
			}
		})
	}
}

func TestReadUnlistableIsUnknown(t *testing.T) {
	h := newHive().app("microphone", "a", false, ft(t0), 0)
	h.keys[storeRoot+"webcam"] = nil
	h.broken[storeRoot+"webcam"] = true
	r := Read(h)
	if r.Camera != Unknown || r.Microphone != InUse {
		t.Fatalf("got %+v", r)
	}
}

func TestWireNeverNamesApps(t *testing.T) {
	r := Read(newHive().app("webcam", `C:#Users#me#AppData#Secret#spy.exe`, true, ft(t0), 0))
	w := r.Wire(zone)
	if w.Camera != InUse || w.Microphone != Idle || w.Since != "2026-10-01T20:00:00+09:00" {
		t.Fatalf("wire %+v", w)
	}
	for _, s := range []string{w.Camera, w.Microphone, w.Since, r.LogLine()} {
		if strings.Contains(strings.ToLower(s), "spy") {
			t.Fatalf("an app name leaked: %q", s)
		}
	}
	if got := UnknownReading().Wire(zone); got.Since != "" || got.Camera != Unknown {
		t.Errorf("unknown wire %+v", got)
	}
}

func TestMonitorScan(t *testing.T) {
	hive := newHive()
	closed := 0
	hive.closed = &closed
	sid, sidErr := "S-1-5-21-1", error(nil)
	var openErr error
	m := &Monitor{
		User: func() (string, error) { return sid, sidErr },
		Open: func(s string) (Hive, error) {
			if s != sid {
				t.Fatalf("opened %s", s)
			}
			return hive, openErr
		},
	}
	if c := m.Current(); c.Camera != Unknown {
		t.Fatalf("before a scan: %+v", c)
	}
	// unknown → idle is a change.
	if r, changed := m.Scan(); !changed || r.Camera != Idle {
		t.Fatalf("first scan %+v changed=%v", r, changed)
	}
	if _, changed := m.Scan(); changed {
		t.Fatal("same state reported as changed")
	}
	hive.app("webcam", "a", false, ft(t0), 0)
	if r, changed := m.Scan(); !changed || r.Camera != InUse {
		t.Fatalf("camera on: %+v %v", r, changed)
	}
	// Only Since moving (a second app joining earlier) is not a change.
	hive.app("webcam", "b", true, ft(t0.Add(-time.Minute)), 0)
	if _, changed := m.Scan(); changed {
		t.Fatal("a Since change counted as a state change")
	}
	if closed != 4 {
		t.Errorf("hive closed %d times, want 4", closed)
	}
	// Hive not loaded / nobody logged in: unknown.
	openErr = windows.ERROR_FILE_NOT_FOUND
	if r, changed := m.Scan(); !changed || r.Camera != Unknown || r.Microphone != Unknown {
		t.Fatalf("no hive: %+v %v", r, changed)
	}
	openErr, sidErr = nil, errors.New("no user")
	if r, _ := m.Scan(); r.Camera != Unknown {
		t.Fatalf("no user: %+v", r)
	}
	m.Reset()
	if c := m.Current(); c.Camera != Unknown {
		t.Fatalf("after reset: %+v", c)
	}
}

func TestJudge(t *testing.T) {
	cam := Reading{Camera: InUse, Microphone: Idle}
	both := Reading{Camera: InUse, Microphone: InUse}
	none := Reading{Camera: Idle, Microphone: Unknown}
	cases := []struct {
		guard string
		r     Reading
		want  Verdict
	}{
		{"off", both, Verdict{}},
		{"warn", none, Verdict{}},
		{"block", none, Verdict{}},
		{"warn", cam, Verdict{InUse: []string{Camera}}},
		{"block", both, Verdict{InUse: []string{Camera, Microphone}, Block: true}},
		{"nonsense", both, Verdict{}},
	}
	for _, c := range cases {
		got := Judge(c.guard, c.r)
		if got.Block != c.want.Block || !slices.Equal(got.InUse, c.want.InUse) {
			t.Errorf("Judge(%s, %+v) = %+v, want %+v", c.guard, c.r, got, c.want)
		}
	}
	if v := Judge("warn", both); !v.Warn() || v.Devices() != "camera,microphone" {
		t.Errorf("warn verdict %+v", v)
	}
}

// TestOpenUserHiveReadsOwnStore reads (never writes) this test user's own
// consent store through HKU, the path the service takes for the target
// user. It only checks that the read works and yields valid states.
func TestOpenUserHiveReadsOwnStore(t *testing.T) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Skip(err)
	}
	h, err := OpenUserHive(u.User.Sid.String())
	if err != nil {
		t.Skipf("own hive not under HKU: %v", err)
	}
	defer h.Close()
	r := Read(h)
	for _, s := range []string{r.Camera, r.Microphone} {
		if s != InUse && s != Idle && s != Unknown {
			t.Errorf("state %q", s)
		}
	}
	if _, err := OpenUserHive("S-1-5-21-0-0-0-424242"); err == nil {
		t.Error("a SID with no loaded hive opened")
	}
}
