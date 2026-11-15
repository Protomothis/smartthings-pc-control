package service

// Starting a missing tray app (#167), wired as the service runs it: the
// real runner and launcher over faked WTS lookups, a real broker, and a
// fake start that brings up a goroutine standing in for the tray.

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/service/session"
)

// fakeLauncher makes trayLauncher (over b) start trays with start, and
// returns the sessions it was asked to start.
func fakeLauncher(t *testing.T, b *session.Broker, start func(uint32) error) func() []uint32 {
	t.Helper()
	var mu sync.Mutex
	var started []uint32
	l := session.NewLauncher(b, func(id uint32) error {
		mu.Lock()
		started = append(started, id)
		mu.Unlock()
		return start(id)
	}, func(id uint32) (session.LogonInfo, error) {
		return session.LogonInfo{ID: "logon", SID: "S-1-5-21-1"}, nil
	})
	l.Wait = 500 * time.Millisecond
	trayLauncher = l
	return func() []uint32 {
		mu.Lock()
		defer mu.Unlock()
		return append([]uint32(nil), started...)
	}
}

func TestCommandStartsMissingTray(t *testing.T) {
	active := uint32(windows.WTSActive)
	b := freshTrays(t)
	fakeWTS(t, 1, []wtsSession{{ID: 1, State: active}}, nil, map[uint32]error{1: nil})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	started := fakeLauncher(t, b, func(id uint32) error {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if job, ok := b.Poll(ctx, id, 77); ok {
					_ = b.Reply(id, job.ID, []byte(`{"ok":true,"audio":{"volume":5,"muted":true,"device":"스피커"}}`))
				}
			}
		}()
		return nil
	})

	res, err := runUserAction(context.Background(), "audio", "mute", "on")
	if err != nil || res.Audio == nil || !res.Audio.Muted {
		t.Fatalf("%+v %v", res, err)
	}
	if got := started(); !reflect.DeepEqual(got, []uint32{1}) {
		t.Errorf("started %v, want [1]", got)
	}
}

func TestCommandTrayStartFailsIsTrayUnavailable(t *testing.T) {
	active := uint32(windows.WTSActive)
	b := freshTrays(t)
	fakeWTS(t, 1, []wtsSession{{ID: 1, State: active}}, nil, map[uint32]error{1: nil})
	started := fakeLauncher(t, b, func(uint32) error { return errors.New("blocked") })
	for range 2 {
		if _, err := runUserAction(context.Background(), "audio", "mute", "on"); !errors.Is(err, session.ErrTrayUnavailable) {
			t.Fatalf("err = %v, want ErrTrayUnavailable", err)
		}
	}
	if !trayMissing(sessionActiveUser) {
		t.Error("trayMissing is false: Telegram would not say the tray is off")
	}
	if got := started(); len(got) != 1 {
		t.Errorf("started %v, want one attempt (the second is held)", got)
	}
}

// Logged off: no start, the answer stays no_user_session.
func TestCommandLoggedOffStartsNothing(t *testing.T) {
	b := freshTrays(t)
	fakeWTS(t, 1, []wtsSession{{ID: 1, State: uint32(windows.WTSActive)}}, nil, map[uint32]error{})
	started := fakeLauncher(t, b, func(uint32) error { return nil })
	if _, err := runUserAction(context.Background(), "audio", "mute", "on"); !errors.Is(err, errNoUserSession) {
		t.Fatalf("err = %v, want errNoUserSession", err)
	}
	if got := started(); len(got) != 0 {
		t.Errorf("started %v while logged off", got)
	}
}

// The status fill's readings run WithoutLaunch: no start.
func TestBackgroundReadingStartsNothing(t *testing.T) {
	b := freshTrays(t)
	fakeWTS(t, 1, []wtsSession{{ID: 1, State: uint32(windows.WTSActive)}}, nil, map[uint32]error{1: nil})
	started := fakeLauncher(t, b, func(uint32) error { return nil })
	_, err := userActions.Run(session.WithoutLaunch(context.Background()), sessionActiveUser, "audio", "get")
	if !errors.Is(err, session.ErrTrayUnavailable) {
		t.Fatalf("err = %v, want ErrTrayUnavailable", err)
	}
	if got := started(); len(got) != 0 {
		t.Errorf("a background reading started %v", got)
	}
}

// The start-up launch looks where commands go: FindUser's choice and the
// console, nothing else.
func TestTrayLaunchSessions(t *testing.T) {
	active, disc := uint32(windows.WTSActive), uint32(windows.WTSDisconnected)
	cases := []struct {
		name     string
		console  uint32
		sessions []wtsSession
		tokens   map[uint32]error
		locked   map[uint32]bool
		want     []uint32
	}{
		{
			name:     "one console user",
			console:  1,
			sessions: []wtsSession{{ID: 0, State: disc}, {ID: 1, State: active}},
			tokens:   map[uint32]error{1: nil},
			locked:   map[uint32]bool{1: false},
			want:     []uint32{1},
		},
		{
			name:    "locked console, unlocked RDP: both, the other RDP and the disconnected session not",
			console: 1,
			sessions: []wtsSession{{ID: 1, State: active}, {ID: 2, State: active}, {ID: 3, State: disc},
				{ID: 4, State: active}},
			tokens: map[uint32]error{1: nil, 2: nil, 3: nil, 4: nil},
			locked: map[uint32]bool{1: true, 2: false, 3: false, 4: true},
			want:   []uint32{2, 1},
		},
		{
			name:     "console at the logon screen, RDP user",
			console:  1,
			sessions: []wtsSession{{ID: 1, State: active}, {ID: 2, State: active}},
			tokens:   map[uint32]error{2: nil},
			locked:   map[uint32]bool{2: false},
			want:     []uint32{2},
		},
		{
			name:     "nobody logged on",
			console:  1,
			sessions: []wtsSession{{ID: 1, State: active}},
			tokens:   map[uint32]error{},
			want:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeWTS(t, tc.console, tc.sessions, nil, tc.tokens)
			fakeLocks(t, tc.locked)
			if got := trayLaunchSessions(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
