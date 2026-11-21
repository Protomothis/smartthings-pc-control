package gui

// Media and audio read diagnostics (#176). Every 3s the tray reads the
// default playback device and the system media session (idle.go), and a
// failed read used to vanish without a trace: the next tick retries. That
// left reports like "high CPU while Apple Music plays" with nothing to go
// on. readDiag times every read and writes to gui.log
//
//   - a line for a slow read (over readSlow) or a failure, naming the app
//     and, for the media session, how long each step took — rate-limited,
//     and a failure that repeats the previous one is not logged again;
//   - a summary every readSummaryEvery while reads happen: count, average,
//     maximum, slow and failed per kind, the heartbeat posts and the apps
//     seen. Never the track: only the app's name.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	// readSlow is a read worth a line: the media session read normally
	// takes a few milliseconds, and its own timeouts are 1–1.5s.
	readSlow = 500 * time.Millisecond
	// readLinesPer and readLineWindow cap the slow/failed lines, so a
	// player that stalls every read costs a handful of lines per window
	// plus one saying how many were left out.
	readLinesPer   = 5
	readLineWindow = 10 * time.Minute
	// readSummaryEvery is how often the counts are summed up.
	readSummaryEvery = 30 * time.Minute
	// maxSummaryApps bounds the app list of a summary.
	maxSummaryApps = 5
)

// readStat counts the reads of one kind since the last summary.
type readStat struct {
	n, slow, failed int
	// trackFailed is a media read that got the status but not the track
	// (TryGetMediaPropertiesAsync failed): still a success for the
	// heartbeat, so it is counted apart.
	trackFailed int
	total, max  time.Duration
	// lastErr is the last failure logged, so the same one is not logged
	// every 3s; a success clears it.
	lastErr string
}

func (s *readStat) add(took time.Duration) {
	s.n++
	s.total += took
	s.max = max(s.max, took)
	if took > readSlow {
		s.slow++
	}
}

func (s readStat) String() string {
	if s.n == 0 {
		return "n=0"
	}
	out := fmt.Sprintf("n=%d avg=%v max=%v slow=%d failed=%d",
		s.n, roundMS(s.total/time.Duration(s.n)), roundMS(s.max), s.slow, s.failed)
	if s.trackFailed > 0 {
		out += fmt.Sprintf(" track-failed=%d", s.trackFailed)
	}
	return out
}

// readDiag is the tray's record of its media and audio reads and its
// heartbeat posts. Safe for concurrent use: the 30s heartbeat and the 3s
// check run on their own goroutines.
type readDiag struct {
	mu   sync.Mutex
	now  func() time.Time
	logf func(format string, args ...any)

	media, audio      readStat
	posts, postFailed int
	apps              map[string]bool // media apps seen since the summary
	app               string          // the media app last seen

	since      time.Time // start of the summary period
	window     time.Time // start of the line window
	lines      int
	suppressed int
}

func newReadDiag(now func() time.Time, logf func(string, ...any)) *readDiag {
	return &readDiag{now: now, logf: logf}
}

// mediaReads is the tray app's readDiag; the tests replace it.
var mediaReads = newReadDiag(time.Now, func(format string, args ...any) { guiLog("media", format, args...) })

// timeAudio runs read, recording how long it took and whether it failed.
func (d *readDiag) timeAudio(read func() (useraction.Audio, error)) (useraction.Audio, error) {
	start := d.now()
	a, err := read()
	took := d.now().Sub(start)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.begin()
	d.audio.add(took)
	switch {
	case err != nil:
		d.audio.failed++
		if msg := err.Error(); msg != d.audio.lastErr {
			d.audio.lastErr = msg
			d.line("audio read failed after %v: %v", roundMS(took), err)
		}
	case took > readSlow:
		d.audio.lastErr = ""
		d.line("audio read took %v", roundMS(took))
	default:
		d.audio.lastErr = ""
	}
	d.maybeSummarize()
	return a, err
}

// timeMedia runs read, recording how long it and each of its steps took,
// whether it failed and which app was playing.
func (d *readDiag) timeMedia(read func() (useraction.NowPlaying, useraction.MediaReadSteps, error)) (useraction.NowPlaying, error) {
	start := d.now()
	np, steps, err := read()
	took := d.now().Sub(start)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.begin()
	d.media.add(took)
	app := np.App
	if app == "" {
		app = useraction.AppDisplayName(steps.AppID)
	}
	if app != "" {
		d.app = app
		if d.apps == nil {
			d.apps = map[string]bool{}
		}
		if len(d.apps) < maxSummaryApps {
			d.apps[app] = true
		}
	}
	detail := mediaStepDetail(steps, app)
	switch {
	case err != nil:
		d.media.failed++
		if msg := err.Error(); msg != d.media.lastErr {
			d.media.lastErr = msg
			d.line("media read failed after %v (%s): %v", roundMS(took), detail, err)
		}
	case steps.PropertiesErr != nil:
		d.media.trackFailed++
		if msg := "track: " + steps.PropertiesErr.Error(); msg != d.media.lastErr {
			d.media.lastErr = msg
			d.line("media read got no track after %v (%s): %v", roundMS(took), detail, steps.PropertiesErr)
		}
	case took > readSlow:
		d.media.lastErr = ""
		d.line("media read took %v (%s)", roundMS(took), detail)
	default:
		d.media.lastErr = ""
	}
	d.maybeSummarize()
	return np, err
}

// mediaStepDetail is the step breakdown of a media read: which call took
// the time, and for which app.
func mediaStepDetail(s useraction.MediaReadSteps, app string) string {
	if app == "" {
		app = "-"
	}
	return fmt.Sprintf("open %v, status %v, properties %v, app %s",
		roundMS(s.Open), roundMS(s.Status), roundMS(s.Properties), app)
}

// notePost records the outcome of one heartbeat post. A failed post is
// the normal state while the service restarts, so it is only counted.
func (d *readDiag) notePost(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.begin()
	d.posts++
	if err != nil {
		d.postFailed++
	}
	d.maybeSummarize()
}

// begin starts the periods on the first event. Callers hold mu.
func (d *readDiag) begin() {
	if d.since.IsZero() {
		now := d.now()
		d.since, d.window = now, now
	}
}

// line logs a slow or failed read unless the window's share is used up.
// Callers hold mu.
func (d *readDiag) line(format string, args ...any) {
	d.rollWindow()
	if d.lines >= readLinesPer {
		d.suppressed++
		return
	}
	d.lines++
	d.logf(format, args...)
}

// rollWindow starts a new line window once the old one is over, saying
// how many lines it left out. Callers hold mu.
func (d *readDiag) rollWindow() {
	now := d.now()
	if now.Sub(d.window) < readLineWindow {
		return
	}
	if d.suppressed > 0 {
		d.logf("%d more slow or failed reads in the last %s were not logged", d.suppressed, minutes(now.Sub(d.window)))
	}
	d.window, d.lines, d.suppressed = now, 0, 0
}

// maybeSummarize writes the summary once the period is over and starts
// the next. Callers hold mu.
func (d *readDiag) maybeSummarize() {
	now := d.now()
	if now.Sub(d.since) < readSummaryEvery {
		return
	}
	d.rollWindow()
	apps := make([]string, 0, len(d.apps))
	for a := range d.apps {
		apps = append(apps, a)
	}
	sort.Strings(apps)
	appList := strings.Join(apps, ", ")
	if appList == "" {
		appList = "-"
	}
	d.logf("reads in the last %s: media %v; audio %v; posts n=%d failed=%d; apps %s (last %s)",
		minutes(now.Sub(d.since)), d.media, d.audio, d.posts, d.postFailed, appList, orDash(d.app))
	media, audio := d.media.lastErr, d.audio.lastErr
	d.media, d.audio = readStat{lastErr: media}, readStat{lastErr: audio}
	d.posts, d.postFailed, d.apps = 0, 0, nil
	d.since = now
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// minutes is a period in whole minutes: "30m" rather than "30m0s".
func minutes(d time.Duration) string {
	return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
}

// roundMS keeps log durations readable: 1.234s, 12ms.
func roundMS(d time.Duration) time.Duration { return d.Round(time.Millisecond) }
