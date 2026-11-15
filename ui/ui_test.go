package ui

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/gui"
)

// fakeAPI answers the reads a page makes when it opens with an error, so
// the sample data stays; any other call panics on the nil API.
type fakeAPI struct{ API }

var errFake = errors.New("fake")

func (fakeAPI) GetSTHub() (gui.STHub, error)                       { return gui.STHub{}, errFake }
func (fakeAPI) TelegramState() (gui.TelegramState, error)          { return gui.TelegramState{}, errFake }
func (fakeAPI) Logs() ([]string, error)                            { return nil, errFake }
func (fakeAPI) GetConfig() (gui.Config, error)                     { return gui.Config{}, errFake }
func (fakeAPI) SaveConfig(gui.Config) (gui.SaveReply, error)       { return gui.SaveReply{}, errFake }
func (fakeAPI) GetWoLStatus() (gui.WoLStatus, error)               { return gui.WoLStatus{}, errFake }
func (fakeAPI) RunningProcesses() ([]string, error)                { return nil, errFake }
func (fakeAPI) TestPreset(gui.Preset) ([]gui.PresetWarning, error) { return nil, errFake }
func (fakeAPI) TestTelegram(_, _ string) error                     { return errFake }
func (fakeAPI) TelegramMe() (string, string, error)                { return "", "", errFake }
func (fakeAPI) TelegramChats(string) ([]gui.TelegramChat, error)   { return nil, errFake }
func (fakeAPI) TestNotify() (gui.NotifyResult, error)              { return gui.NotifyResult{}, errFake }

// literalKey finds the keys looked up by literal: u.t("…") and
// gui.T(l, "…").
var literalKey = regexp.MustCompile(`(?:\bu\.t\(|\bgui\.T\([A-Za-z.]+, )"([a-z0-9_.]+)"\)`)

// Every key the window asks for exists in both languages; gui.T would
// otherwise show the key itself.
func TestLocaleKeysExist(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	seen := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range literalKey.FindAllStringSubmatch(string(src), -1) {
			seen++
			for _, l := range gui.Languages() {
				if gui.T(l, m[1]) == m[1] {
					t.Errorf("%s: %q is not in %s.json", f, m[1], l)
				}
			}
		}
	}
	if seen < 50 {
		t.Errorf("found only %d literal lookups; is the pattern stale?", seen)
	}
	for _, kind := range []string{"program", "url", "script"} {
		if gui.T(gui.LangKo, "presets.type."+kind) == "presets.type."+kind {
			t.Errorf("presets.type.%s missing", kind)
		}
	}
}

func TestPNGToICO(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	ico := pngToICO(b.Bytes())
	if len(ico) != 22+b.Len() || ico[2] != 1 || ico[4] != 1 || ico[6] != 32 || ico[18] != 22 {
		t.Fatalf("bad header % x", ico[:22])
	}
	if pngToICO([]byte("not a png")) != nil {
		t.Fatal("garbage became an icon")
	}
}

func TestPrefsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fyne", appID, "preferences.json")
	p := loadPrefs(path)
	if p.String(prefTheme, "x") != "x" || !p.Bool(prefAutostart, true) {
		t.Fatal("fallbacks not used on a missing file")
	}
	p.Set(prefTheme, "dark")
	p.Set(prefAutostart, false)
	q := loadPrefs(path)
	if q.String(prefTheme, "") != "dark" || q.Bool(prefAutostart, true) {
		t.Fatalf("not kept: %v", q.vals)
	}
}

func TestWantDark(t *testing.T) {
	u := New(fakeAPI{}, "dev", loadPrefs(""), func() {})
	for _, c := range []struct {
		pref      string
		sys, want bool
	}{{"", true, true}, {"", false, false}, {"light", true, false}, {"dark", false, true}} {
		u.prefs.Set(prefTheme, c.pref)
		if got := u.wantDark(c.sys); got != c.want {
			t.Errorf("theme %q, system dark %v: got %v", c.pref, c.sys, got)
		}
	}
}

func TestScheduleRemaining(t *testing.T) {
	now := time.Now()
	s := snapshot{sched: gui.Schedule{Active: true, RemainingSec: 90}, schedAt: now}
	if got := s.remaining(now.Add(30 * time.Second)); got != time.Minute {
		t.Fatalf("remaining = %v", got)
	}
	if got := s.remaining(now.Add(5 * time.Minute)); got != 0 {
		t.Fatalf("past the end: %v", got)
	}
}
