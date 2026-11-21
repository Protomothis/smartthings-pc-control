package gui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// fakeReadClock is a clock the fake reads move on.
type fakeReadClock struct{ t time.Time }

func (c *fakeReadClock) now() time.Time       { return c.t }
func (c *fakeReadClock) pass(d time.Duration) { c.t = c.t.Add(d) }
func newFakeReadClock() *fakeReadClock {
	return &fakeReadClock{t: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}
}
func collectLines(lines *[]string) func(string, ...any) {
	return func(f string, a ...any) { *lines = append(*lines, fmt.Sprintf(f, a...)) }
}

// mediaRead is a fake media session read that takes took.
func mediaRead(c *fakeReadClock, took time.Duration, np useraction.NowPlaying, steps useraction.MediaReadSteps, err error) func() (useraction.NowPlaying, useraction.MediaReadSteps, error) {
	return func() (useraction.NowPlaying, useraction.MediaReadSteps, error) {
		c.pass(took)
		return np, steps, err
	}
}

func audioRead(c *fakeReadClock, took time.Duration, err error) func() (useraction.Audio, error) {
	return func() (useraction.Audio, error) { c.pass(took); return useraction.Audio{Volume: 10}, err }
}

func TestReadDiagSlowAndFailedLines(t *testing.T) {
	c := newFakeReadClock()
	var lines []string
	d := newReadDiag(c.now, collectLines(&lines))
	playing := useraction.NowPlaying{Status: useraction.MediaPlaying, Title: "secret song", App: "Apple Music"}

	// A quick read is silent.
	d.timeMedia(mediaRead(c, 5*time.Millisecond, playing, useraction.MediaReadSteps{}, nil))
	d.timeAudio(audioRead(c, 2*time.Millisecond, nil))
	if len(lines) != 0 {
		t.Fatalf("quick reads logged: %q", lines)
	}

	// A slow read names the step that stalled and the app, never the track.
	steps := useraction.MediaReadSteps{Open: 20 * time.Millisecond, Status: time.Millisecond, Properties: 980 * time.Millisecond}
	d.timeMedia(mediaRead(c, time.Second, playing, steps, nil))
	if len(lines) != 1 || lines[0] != "media read took 1s (open 20ms, status 1ms, properties 980ms, app Apple Music)" {
		t.Errorf("slow: %q", lines)
	}
	if strings.Contains(strings.Join(lines, "\n"), "secret") {
		t.Error("the track reached the log")
	}

	// A failure is logged once until something else happens; the app
	// comes from the AUMID when the read itself has none.
	failSteps := useraction.MediaReadSteps{Open: 1500 * time.Millisecond, AppID: "AppleInc.AppleMusicWin_nzyj5cx40ttqa!App"}
	failure := errors.New("media session: SessionManager.RequestAsync: no result within 1.5s")
	for range 3 {
		d.timeMedia(mediaRead(c, 1500*time.Millisecond, useraction.NowPlaying{}, failSteps, failure))
	}
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "media read failed after 1.5s (open 1.5s, status 0s, properties 0s, app Apple Music): media session: SessionManager") {
		t.Errorf("failure: %q", lines)
	}

	// The track failing is its own line, while the read still succeeds.
	trackSteps := useraction.MediaReadSteps{PropertiesErr: errors.New("TryGetMediaPropertiesAsync: no result within 1s")}
	if _, err := d.timeMedia(mediaRead(c, 10*time.Millisecond, playing, trackSteps, nil)); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || !strings.Contains(lines[2], "media read got no track") {
		t.Errorf("track: %q", lines)
	}

	d.timeAudio(audioRead(c, 3*time.Millisecond, errors.New("audio: no playback device")))
	d.timeAudio(audioRead(c, 3*time.Millisecond, errors.New("audio: no playback device")))
	if len(lines) != 4 || lines[3] != "audio read failed after 3ms: audio: no playback device" {
		t.Errorf("audio: %q", lines)
	}
}

func TestReadDiagRateLimit(t *testing.T) {
	c := newFakeReadClock()
	var lines []string
	d := newReadDiag(c.now, collectLines(&lines))
	for range 8 {
		d.timeAudio(audioRead(c, 600*time.Millisecond, nil))
		c.pass(3 * time.Second)
	}
	if len(lines) != readLinesPer {
		t.Fatalf("%d lines in one window: %q", len(lines), lines)
	}
	c.pass(readLineWindow)
	d.timeAudio(audioRead(c, 600*time.Millisecond, nil))
	if len(lines) != readLinesPer+2 || !strings.HasPrefix(lines[readLinesPer], "3 more slow or failed reads") {
		t.Errorf("after the window: %q", lines[readLinesPer:])
	}
}

func TestReadDiagSummary(t *testing.T) {
	c := newFakeReadClock()
	var lines []string
	d := newReadDiag(c.now, collectLines(&lines))
	playing := useraction.NowPlaying{Status: useraction.MediaPlaying, App: "Apple Music"}
	d.timeMedia(mediaRead(c, 10*time.Millisecond, playing, useraction.MediaReadSteps{}, nil))
	d.timeMedia(mediaRead(c, 30*time.Millisecond, useraction.NowPlaying{Status: useraction.MediaPlaying, App: "Chrome"}, useraction.MediaReadSteps{}, nil))
	d.timeAudio(audioRead(c, 4*time.Millisecond, nil))
	d.notePost(nil)
	d.notePost(errors.New("connection refused"))
	if len(lines) != 0 {
		t.Fatalf("summary too early: %q", lines)
	}
	c.pass(readSummaryEvery)
	d.timeMedia(mediaRead(c, 20*time.Millisecond, playing, useraction.MediaReadSteps{}, nil))
	want := "reads in the last 30m: media n=3 avg=20ms max=30ms slow=0 failed=0; audio n=1 avg=4ms max=4ms slow=0 failed=0; posts n=2 failed=1; apps Apple Music, Chrome (last Apple Music)"
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("summary:\n got %q\nwant %q", lines, want)
	}
	// The counts start over.
	c.pass(readSummaryEvery)
	d.notePost(nil)
	if len(lines) != 2 || !strings.Contains(lines[1], "media n=0; audio n=0; posts n=1 failed=0; apps - (last Apple Music)") {
		t.Errorf("second summary: %q", lines)
	}
}
