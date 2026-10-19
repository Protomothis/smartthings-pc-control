package gui

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// The flag turns on with the first loud reading, stays on through up to
// playingHold of silence, turns off after it, and is unknown while the
// meter cannot be read (#159).
func TestPlayingTracker(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	var tr playingTracker
	steps := []struct {
		at      time.Duration
		peak    float32
		err     error
		want    playState
		changed bool
	}{
		{0, 0, nil, playIdle, true}, // silence from the start: not playing at once
		{300 * time.Millisecond, 0.0005, nil, playIdle, false},
		{600 * time.Millisecond, 0.02, nil, playOn, true},
		{900 * time.Millisecond, 0, nil, playOn, false},
		{5 * time.Second, 0, nil, playOn, false}, // 4.4s of silence
		{5*time.Second + 300*time.Millisecond, 0.3, nil, playOn, false},
		{10*time.Second + 200*time.Millisecond, 0, nil, playOn, false},
		{10*time.Second + 300*time.Millisecond, 0, nil, playIdle, true}, // 5s since the last sound
		{11 * time.Second, 0, errors.New("no device"), playUnknown, true},
		{12 * time.Second, 0.5, nil, playOn, true},
	}
	for i, s := range steps {
		changed := tr.observe(s.peak, s.err, t0.Add(s.at))
		if tr.state != s.want || changed != s.changed {
			t.Errorf("step %d (%v): state %d changed %v, want %d %v", i, s.at, tr.state, changed, s.want, s.changed)
		}
	}
	if playingFlag(playUnknown) != nil || !*playingFlag(playOn) || *playingFlag(playIdle) {
		t.Error("playingFlag")
	}
}

// The heartbeat's audio block carries the recording device and the flag;
// a change of either is a change the 3s check posts.
func TestHeartbeatAudioCarriesInputAndPlaying(t *testing.T) {
	spk := useraction.Audio{Volume: 30, Device: "스피커", Input: useraction.AudioInput{Present: true, Device: "마이크", Muted: true}}
	fakeHeartbeat(t, false, true, spk, nil)
	on := true
	heartbeatPlaying = func() *bool { v := on; return &v }
	hb, _ := buildHeartbeat()
	b, _ := json.Marshal(hb.Audio)
	want := `{"volume":30,"muted":false,"device":"스피커","input":{"present":true,"device":"마이크","muted":true},"playing":true,"sampled_at":"2026-09-30T21:00:07.25+09:00"}`
	if string(b) != want {
		t.Errorf("audio block = %s\nwant          %s", b, want)
	}

	var sent heartbeatSent
	sent.delivered(hb)
	if _, ok := sent.changes(sampleMediaBlocks()); ok {
		t.Error("an unchanged block reads as a change")
	}
	on = false
	if c, ok := sent.changes(sampleMediaBlocks()); !ok || c.Audio == nil || *c.Audio.Playing {
		t.Errorf("playing off: %v %+v", ok, c.Audio)
	}
	heartbeatPlaying = func() *bool { return nil }
	if c, ok := sent.changes(sampleMediaBlocks()); !ok || c.Audio.Playing != nil {
		t.Errorf("playing unknown: %v %+v", ok, c.Audio)
	}
	if b, _ := json.Marshal(HeartbeatAudio{Volume: 1}); strings.Contains(string(b), "playing") || strings.Contains(string(b), "input") {
		t.Errorf("unknown parts are sent: %s", b)
	}
}

// heartbeatRecorder is a service that records the heartbeats it gets.
type heartbeatRecorder struct {
	serviceAPI
	mu  sync.Mutex
	got []Heartbeat
}

func (h *heartbeatRecorder) SessionHeartbeat(hb Heartbeat) (bool, error) {
	h.mu.Lock()
	h.got = append(h.got, hb)
	h.mu.Unlock()
	return false, nil
}

func (h *heartbeatRecorder) posts() []Heartbeat {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Heartbeat(nil), h.got...)
}

// The meter loop: a reading that flips the flag posts the audio block with
// it at once; media control off makes the flag unknown and reads nothing.
func TestPlayingLoop(t *testing.T) {
	fakeHeartbeat(t, false, true, useraction.Audio{Volume: 30, Device: "스피커"}, nil)
	var mediaOn atomic.Bool
	mediaOn.Store(true)
	heartbeatMediaEnabled = mediaOn.Load
	heartbeatPlaying = currentPlaying
	setPlaying(playUnknown)
	t.Cleanup(func() { setPlaying(playUnknown) })

	ticks := make(chan time.Time)
	var mu sync.Mutex
	peak, reads := float32(0), 0
	savedTick, savedRead, savedNow, savedChanged := playingTick, readPeak, playingNow, playingChanged
	playingTick = func() (<-chan time.Time, func()) { return ticks, func() {} }
	readPeak = func() (float32, error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		return peak, nil
	}
	now := time.Unix(1_800_000_000, 0)
	playingNow = func() time.Time { return now }
	// The change check runs on the loop's goroutine here, so the test's
	// cleanup (after the loop ended) cannot race it.
	playingChanged = func(u *ui) { u.watchMediaChanges() }
	t.Cleanup(func() {
		playingTick, readPeak, playingNow, playingChanged = savedTick, savedRead, savedNow, savedChanged
	})

	svc := &heartbeatRecorder{}
	u := &ui{client: svc, quit: make(chan struct{})}
	done := make(chan struct{})
	go func() { u.playingLoop(); close(done) }()
	t.Cleanup(func() { close(u.quit); <-done })

	waitPosts := func(n int) []Heartbeat {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for len(svc.posts()) < n && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		return svc.posts()
	}

	ticks <- now // silence: idle, posted
	if p := waitPosts(1); len(p) != 1 || p[0].Audio == nil || p[0].Audio.Playing == nil || *p[0].Audio.Playing {
		t.Fatalf("first reading: %+v", p)
	}
	mu.Lock()
	peak = 0.2
	mu.Unlock()
	ticks <- now // sound: playing, posted
	if p := waitPosts(2); len(p) != 2 || p[1].Audio == nil || p[1].Audio.Playing == nil || !*p[1].Audio.Playing {
		t.Fatalf("sound: %+v", p)
	}
	ticks <- now // still playing: nothing new
	time.Sleep(50 * time.Millisecond)
	if p := svc.posts(); len(p) != 2 {
		t.Errorf("no change posted: %+v", p)
	}

	// Media control off: unknown at the next switch read, no more meter reads.
	mediaOn.Store(false)
	for range playingSwitchEvery + 1 {
		ticks <- now
	}
	ticks <- now // the previous tick is handled once this one is taken
	if p := currentPlaying(); p != nil {
		t.Errorf("flag with media off = %v", *p)
	}
	mu.Lock()
	before := reads
	mu.Unlock()
	ticks <- now
	ticks <- now
	mu.Lock()
	after := reads
	mu.Unlock()
	if after != before {
		t.Errorf("meter read with media off (%d → %d)", before, after)
	}
}
