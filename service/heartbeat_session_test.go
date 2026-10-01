package service

// Tests for the heartbeat's session_id: only the session the commands act
// on may write the idle, audio and media samples. Observed on a machine
// with a locked console session (1) and an RDP session (2) running the
// tray app: mute acted on session 1, and session 2's heartbeat flipped
// the reported state back to unmuted within 20 seconds.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/internal/logx"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// sessionHeartbeatSetup empties the idle, audio and media stores and
// forgets the target session.
func sessionHeartbeatSetup(t *testing.T) {
	t.Helper()
	nowPlayingSetup(t, optIn())
	idleSetup(t)
	resetTargetSession()
	t.Cleanup(resetTargetSession)
}

// fakeTarget makes heartbeatTargetSession answer id, err.
func fakeTarget(t *testing.T, id uint32, err error) {
	t.Helper()
	saved := heartbeatTargetSession
	heartbeatTargetSession = func() (uint32, error) { return id, err }
	t.Cleanup(func() { heartbeatTargetSession = saved })
}

const fullBeat = `"idle_seconds":42,"audio":{"volume":30,"muted":false,"device":"원격 오디오"},"media":{"status":"playing","app":"Spotify"}`

// heartbeatStatus posts body and returns the reply's status and reason.
func heartbeatStatus(t *testing.T, body string) (status, reason string) {
	t.Helper()
	w := heartbeatDo(t, body, true, true)
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat %s: %d (%s)", body, w.Code, w.Body.String())
	}
	var r struct{ Status, Reason string }
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("reply %q: %v", w.Body.String(), err)
	}
	return r.Status, r.Reason
}

func TestHeartbeatFromOtherSessionIgnored(t *testing.T) {
	sessionHeartbeatSetup(t)
	fakeTarget(t, 1, nil)
	// The mute command's reply, from session 1.
	recordAudioSample(useraction.Audio{Volume: 30, Muted: true, Device: "스피커"}, audioNow().Add(-20*time.Second))

	status, reason := heartbeatStatus(t, `{`+fullBeat+`,"session_id":2}`)
	if status != "ignored" || reason != "other_session" {
		t.Errorf("reply = %s/%s, want ignored/other_session", status, reason)
	}
	if s, _ := currentAudio(); !s.Muted || s.Device != "스피커" {
		t.Errorf("audio = %+v: session 2 overwrote session 1's state", s)
	}
	if _, ok := lastIdleSeconds(); ok {
		t.Error("session 2's idle time was stored")
	}
	if _, ok := currentMedia(); ok {
		t.Error("session 2's media was stored")
	}
	// A bad body is still a 400, whatever session it claims.
	if w := heartbeatDo(t, `{"audio":{"volume":101},"session_id":2}`, true, true); w.Code != http.StatusBadRequest {
		t.Errorf("bad body from another session: %d, want 400", w.Code)
	}
}

func TestHeartbeatFromTargetSessionStored(t *testing.T) {
	sessionHeartbeatSetup(t)
	fakeTarget(t, 1, nil)
	if status, _ := heartbeatStatus(t, `{`+fullBeat+`,"session_id":1}`); status != "ok" {
		t.Errorf("status = %q, want ok", status)
	}
	if s, ok := currentAudio(); !ok || s.Volume != 30 || s.Muted {
		t.Errorf("audio = %+v, %v", s, ok)
	}
	if idle, ok := lastIdleSeconds(); !ok || idle != 42 {
		t.Errorf("idle = %d, %v", idle, ok)
	}
	if m, ok := currentMedia(); !ok || m.Status != "playing" {
		t.Errorf("media = %+v, %v", m, ok)
	}
}

// An older tray app sends no session_id and is believed as before; so is
// any heartbeat while the target cannot be found out.
func TestHeartbeatWithoutSessionIDStored(t *testing.T) {
	sessionHeartbeatSetup(t)
	fakeTarget(t, 1, nil)
	if status, _ := heartbeatStatus(t, `{`+fullBeat+`}`); status != "ok" {
		t.Errorf("no session_id: status = %q, want ok", status)
	}
	if _, ok := lastIdleSeconds(); !ok {
		t.Error("no session_id: idle not stored")
	}

	resetIdleHeartbeat()
	fakeTarget(t, 0, errNoUserSession)
	if status, _ := heartbeatStatus(t, `{"idle_seconds":7,"session_id":2}`); status != "ok" {
		t.Errorf("target unknown: status = %q, want ok", status)
	}
	if idle, ok := lastIdleSeconds(); !ok || idle != 7 {
		t.Errorf("target unknown: idle = %d, %v", idle, ok)
	}
}

// The target is the session findUserSession picks — the one runUserAction
// runs the commands in: the console over an RDP login while neither is
// known to be unlocked.
func TestHeartbeatTargetIsCommandSession(t *testing.T) {
	sessionHeartbeatSetup(t)
	fakeWTS(t, 1, []wtsSession{{2, windows.WTSActive}}, nil, map[uint32]error{1: nil, 2: nil})
	if status, _ := heartbeatStatus(t, `{"idle_seconds":5,"session_id":2}`); status != "ignored" {
		t.Errorf("RDP session next to a console user: status = %q, want ignored", status)
	}
	if status, _ := heartbeatStatus(t, `{"idle_seconds":5,"session_id":1}`); status != "ok" {
		t.Errorf("console session: status = %q, want ok", status)
	}
}

// captureLog sends logMsg to a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	t.Cleanup(logx.Capture(&buf))
	return &buf
}

// v1.2.0-rc7: next to a locked console the unlocked RDP session is the
// target, so its tray app's heartbeat is stored and the locked console's
// is not. Locking and unlocking the RDP session moves the target back and
// forth, each move dropping the samples once and logging once.
func TestHeartbeatFromUnlockedRDPAccepted(t *testing.T) {
	sessionHeartbeatSetup(t)
	logs := captureLog(t)
	fakeWTS(t, 1, []wtsSession{{1, windows.WTSActive}, {2, windows.WTSActive}}, nil, map[uint32]error{1: nil, 2: nil})
	locks := map[uint32]bool{1: true, 2: false}
	fakeLocks(t, locks)

	if status, _ := heartbeatStatus(t, `{`+fullBeat+`,"session_id":2}`); status != "ok" {
		t.Errorf("unlocked RDP tray: status = %q, want ok", status)
	}
	if s, ok := currentAudio(); !ok || s.Device != "원격 오디오" {
		t.Errorf("audio = %+v, %v; want the RDP session's", s, ok)
	}
	if status, reason := heartbeatStatus(t, `{"idle_seconds":900,"session_id":1}`); status != "ignored" || reason != "other_session" {
		t.Errorf("locked console tray: reply = %s/%s, want ignored/other_session", status, reason)
	}
	if idle, _ := lastIdleSeconds(); idle != 42 {
		t.Errorf("idle = %d: the locked console overwrote the RDP session's 42", idle)
	}
	// Repeated lookups with nothing changing log nothing.
	for range 5 {
		heartbeatStatus(t, `{"idle_seconds":42,"session_id":2}`)
	}
	if n := strings.Count(logs.String(), "user session changed"); n != 0 {
		t.Errorf("%d target changes logged while nothing changed:\n%s", n, logs)
	}

	// The RDP user locks too: nobody is unlocked, the console is the target.
	locks[2] = true
	if status, _ := heartbeatStatus(t, `{"idle_seconds":900,"session_id":1}`); status != "ok" {
		t.Errorf("all locked: console tray status = %q, want ok", status)
	}
	if _, ok := currentAudio(); ok {
		t.Error("the RDP session's audio survived the move to the console")
	}
	// And unlocks again.
	locks[2] = false
	heartbeatStatus(t, `{"idle_seconds":3,"session_id":2}`)
	heartbeatStatus(t, `{"idle_seconds":4,"session_id":2}`)
	if idle, ok := lastIdleSeconds(); !ok || idle != 4 {
		t.Errorf("after the unlock: idle = %d, %v; want 4", idle, ok)
	}
	if n := strings.Count(logs.String(), "user session changed"); n != 2 {
		t.Errorf("%d target changes logged, want 2 (2→1, 1→2):\n%s", n, logs)
	}
}

// When the target moves, the samples of the old session are dropped.
func TestTargetSessionChangeClearsSamples(t *testing.T) {
	sessionHeartbeatSetup(t)
	store := func() {
		noteIdleHeartbeat(10)
		recordAudioSample(useraction.Audio{Volume: 30, Device: "원격 오디오"}, audioNow())
		recordMediaSample(useraction.NowPlaying{Status: "playing"}, audioNow())
	}
	// stored counts the samples present: idle, audio, media.
	stored := func() int {
		n := 0
		if _, ok := lastIdleSeconds(); ok {
			n++
		}
		if _, ok := currentAudio(); ok {
			n++
		}
		if _, ok := currentMedia(); ok {
			n++
		}
		return n
	}

	store()
	observeTargetSession(2) // first lookup: nothing to compare with
	observeTargetSession(2)
	if n := stored(); n != 3 {
		t.Fatalf("%d of 3 samples kept while the target stayed the same", n)
	}
	observeTargetSession(1)
	if n := stored(); n != 0 {
		t.Errorf("%d samples of session 2 kept after the target moved to 1", n)
	}

	// Through the real lookup: the console user logs off, nobody is left.
	store()
	fakeWTS(t, 1, nil, nil, map[uint32]error{1: nil})
	if id, err := targetUserSession(); err != nil || id != 1 {
		t.Fatalf("targetUserSession = %d, %v", id, err)
	}
	if n := stored(); n != 3 {
		t.Fatalf("%d of 3 samples kept for the same target", n)
	}
	fakeWTS(t, 1, nil, nil, nil)
	if _, err := targetUserSession(); !errors.Is(err, errNoUserSession) {
		t.Fatalf("err = %v, want errNoUserSession", err)
	}
	if n := stored(); n != 0 {
		t.Errorf("%d samples kept after the user logged off", n)
	}
}
