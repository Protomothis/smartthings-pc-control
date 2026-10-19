package service

// The speaker and microphone state of #159: playing from the tray
// heartbeat, the recording device from the audio sample, and the pushes
// they set off.

import (
	"sync"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// tapAudioPushes collects the data of the audio.changed pushes.
func tapAudioPushes(t *testing.T) func() []map[string]string {
	t.Helper()
	startNotifier(nil)
	t.Cleanup(stopNotifier)
	var mu sync.Mutex
	var got []map[string]string
	busMu.RLock()
	bus.Tap(func(ev notify.Event) {
		if ev.Key() != "audio.changed" {
			return
		}
		mu.Lock()
		got = append(got, ev.Fields)
		mu.Unlock()
	})
	busMu.RUnlock()
	return func() []map[string]string {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]string(nil), got...)
	}
}

func playingSetup(t *testing.T) {
	t.Helper()
	mediaSetup(t, mediaOn())
	resetPlaying()
	t.Cleanup(resetPlaying)
	t.Cleanup(dev.tray.Reset)
}

func boolp(b bool) *bool { return &b }

// The status block: playing while a tray app reports it, null without one
// or once its flag is older than the heartbeat TTL; the recording device
// from the sample, null while the sample has none.
func TestPlayingAndInputInStatus(t *testing.T) {
	playingSetup(t)
	now := time.Now()
	clock.audio = func() time.Time { return now }
	speaker := useraction.Audio{Volume: 30, Device: "스피커", Input: useraction.AudioInput{Present: true, Device: "마이크", Muted: true}}

	heartbeat.Audio(speaker, boolp(true), now)
	a := stAudioStatus(getConfig())
	if a.Playing == nil || !*a.Playing || a.InputDevice == nil || *a.InputDevice != "마이크" || a.InputMuted == nil || !*a.InputMuted {
		t.Fatalf("status = playing %v input %v/%v", a.Playing, a.InputDevice, a.InputMuted)
	}

	// A command reply has no playing flag and leaves it alone.
	recordAudioSample(useraction.Audio{Volume: 40, Device: "스피커"}, now)
	a = stAudioStatus(getConfig())
	if a.Playing == nil || !*a.Playing {
		t.Errorf("a command reply dropped playing: %v", a.Playing)
	}
	if a.InputDevice != nil || a.InputMuted != nil {
		t.Errorf("a sample without a recording device kept input %v/%v", a.InputDevice, a.InputMuted)
	}

	// The tray app stops reporting: null once the flag is too old.
	now = now.Add(playingTTL + time.Second)
	if a := stAudioStatus(getConfig()); a.Playing != nil {
		t.Errorf("playing after the TTL = %v", *a.Playing)
	}

	// An unknown flag (meter unreadable, older tray app) is null too.
	heartbeat.Audio(speaker, nil, now)
	if a := stAudioStatus(getConfig()); a.Playing != nil {
		t.Errorf("playing from a nil flag = %v", *a.Playing)
	}
}

// The tray app's heartbeat body carries playing and the recording device
// in its audio block; a bad recording-device name is refused like a bad
// playback one.
func TestHeartbeatCarriesPlayingAndInput(t *testing.T) {
	stSetup(t, Config{Port: 5001, Media: MediaConfig{Enabled: true}})
	idleSetup(t)
	playingSetup(t)
	heartbeat.target = func() (uint32, error) { return 1, nil }
	t.Cleanup(func() { heartbeat.target = targetUserSession })

	body := `{"session_id":1,"audio":{"volume":20,"muted":false,"device":"스피커","input":{"present":true,"device":"헤드셋 마이크","muted":true},"playing":true}}`
	if w := heartbeatDo(t, body, true, true); w.Code != 200 {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
	}
	a := stAudioStatus(getConfig())
	if a.Playing == nil || !*a.Playing || a.InputDevice == nil || *a.InputDevice != "헤드셋 마이크" || a.InputMuted == nil || !*a.InputMuted {
		t.Errorf("status after the heartbeat: playing %v input %v/%v", a.Playing, a.InputDevice, a.InputMuted)
	}
	bad := `{"session_id":1,"audio":{"volume":20,"muted":false,"device":"스피커","input":{"present":true,"device":"bad\u0007","muted":true}}}`
	if w := heartbeatDo(t, bad, true, true); w.Code != 400 {
		t.Errorf("bad input name: %d", w.Code)
	}
}

// One push for a heartbeat that changes the sample and playing together;
// one for a recording-device change; the data carries what is known.
func TestPlayingAndInputPushes(t *testing.T) {
	playingSetup(t)
	pushes := tapAudioPushes(t)
	now := time.Now()
	base := useraction.Audio{Volume: 30, Device: "스피커", Input: useraction.AudioInput{Present: true, Device: "마이크"}}

	heartbeat.Audio(base, boolp(false), now) // baseline
	if got := pushes(); len(got) != 0 {
		t.Fatalf("baseline pushed %v", got)
	}
	muted := base
	muted.Muted = true
	heartbeat.Audio(muted, boolp(true), now.Add(time.Second))
	got := pushes()
	if len(got) != 1 || got[0]["muted"] != "true" || got[0]["playing"] != "true" || got[0]["input_device"] != "마이크" || got[0]["input_muted"] != "false" {
		t.Fatalf("sample and playing together: %v", got)
	}

	micMuted := muted
	micMuted.Input.Muted = true
	heartbeat.Audio(micMuted, boolp(true), now.Add(2*time.Second))
	if got := pushes(); len(got) != 2 || got[1]["input_muted"] != "true" {
		t.Errorf("microphone muted: %v", got)
	}

	// No recording device and playing unknown: the keys are left out.
	none := micMuted
	none.Input = useraction.AudioInput{}
	heartbeat.Audio(none, nil, now.Add(3*time.Second))
	got = pushes()
	if len(got) != 3 {
		t.Fatalf("recording device gone: %v", got)
	}
	for _, k := range []string{"input_device", "input_muted", "playing"} {
		if _, ok := got[2][k]; ok {
			t.Errorf("%s in the data while unknown: %v", k, got[2])
		}
	}
}

// A change of playing alone is pushed at most every playingPushGap: the
// first at once, one inside the gap when it ends (with the state of that
// moment), and further changes meanwhile ride on that one.
func TestPlayingPushesSpacedOut(t *testing.T) {
	playingSetup(t)
	saved := playingPushGap
	playingPushGap = 300 * time.Millisecond
	t.Cleanup(func() { playingPushGap = saved })
	pushes := tapAudioPushes(t)
	now := time.Now()
	clock.audio = func() time.Time { return now }
	a := useraction.Audio{Volume: 30, Device: "스피커"}

	heartbeat.Audio(a, boolp(false), now) // baseline
	heartbeat.Audio(a, boolp(true), now)
	if got := pushes(); len(got) != 1 || got[0]["playing"] != "true" {
		t.Fatalf("first change: %v", got)
	}
	heartbeat.Audio(a, boolp(false), now)
	heartbeat.Audio(a, boolp(true), now)
	heartbeat.Audio(a, boolp(false), now)
	if got := pushes(); len(got) != 1 {
		t.Fatalf("changes inside the gap pushed at once: %v", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(pushes()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := pushes()
	if len(got) != 2 || got[1]["playing"] != "false" {
		t.Fatalf("after the gap: %v", got)
	}
	time.Sleep(playingPushGap + 100*time.Millisecond)
	if got := pushes(); len(got) != 2 {
		t.Errorf("more than one delayed push: %v", got)
	}

	// A volume push carries playing and cancels a waiting playing push.
	louder := a
	louder.Volume = 50
	heartbeat.Audio(louder, boolp(false), now) // pushes: volume
	heartbeat.Audio(louder, boolp(true), now)  // inside the gap: waits
	loudest := louder
	loudest.Volume = 60
	heartbeat.Audio(loudest, boolp(true), now) // pushes: volume, playing true
	time.Sleep(playingPushGap + 100*time.Millisecond)
	got = pushes()
	if len(got) != 4 || got[3]["volume"] != "60" || got[3]["playing"] != "true" {
		t.Errorf("volume push over a waiting playing push: %v", got)
	}
}
