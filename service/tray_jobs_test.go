package service

// The user-session actions go to the tray app of the target session
// (#164): the real runner (trayExec) over faked WTS lookups and a real
// broker with a goroutine standing in for the tray.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/Protomothis/smartthings-pc-control/service/action"
	"github.com/Protomothis/smartthings-pc-control/service/session"
)

// freshTrays gives the test a broker of its own.
func freshTrays(t *testing.T) *session.Broker {
	t.Helper()
	saved := trays
	trays = session.NewBroker()
	resetTargetSession()
	resetAudioSample()
	t.Cleanup(func() {
		trays = saved
		resetTargetSession()
		resetAudioSample()
	})
	return trays
}

// fakeTray answers every job of session with reply until the test ends,
// and waits until it counts as connected.
func fakeTray(t *testing.T, b *session.Broker, sessionID uint32, reply string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			if job, ok := b.Poll(ctx, sessionID, 4242); ok {
				_ = b.Reply(sessionID, job.ID, []byte(reply))
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(2 * time.Second)
	for !b.Connected(sessionID) {
		if time.Now().After(deadline) {
			t.Fatal("the fake tray never connected")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestUserActionsGoToTheTray(t *testing.T) {
	active := uint32(windows.WTSActive)

	t.Run("logged off: no_user_session, tray or not", func(t *testing.T) {
		b := freshTrays(t)
		fakeWTS(t, 1, []wtsSession{{ID: 1, State: active}}, nil, map[uint32]error{})
		fakeTray(t, b, 1, `{"ok":true}`)
		_, err := runUserAction(context.Background(), "audio", "mute", "on")
		if !errors.Is(err, errNoUserSession) {
			t.Fatalf("err = %v, want errNoUserSession", err)
		}
		if f := action.Classify(err); f.Status != http.StatusConflict || f.Code != "no_user_session" {
			t.Errorf("answer %d %s", f.Status, f.Code)
		}
	})

	t.Run("logged in, no tray: tray_unavailable at once", func(t *testing.T) {
		freshTrays(t)
		fakeWTS(t, 1, []wtsSession{{ID: 1, State: active}}, nil, map[uint32]error{1: nil})
		start := time.Now()
		_, err := runUserAction(context.Background(), "audio", "mute", "on")
		if !errors.Is(err, session.ErrTrayUnavailable) {
			t.Fatalf("err = %v, want ErrTrayUnavailable", err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("took %v; it should fail fast", d)
		}
		if f := action.Classify(err); f.Status != http.StatusConflict || f.Code != "tray_unavailable" {
			t.Errorf("answer %d %s", f.Status, f.Code)
		}
		if trayConnected(sessionActiveUser) {
			t.Error("trayConnected without a tray")
		}
	})

	t.Run("the target session's tray runs it", func(t *testing.T) {
		b := freshTrays(t)
		// A locked console (1) next to an unlocked RDP session (2): the
		// command targets 2, the screen commands the console.
		fakeWTS(t, 1, []wtsSession{{ID: 1, State: active}, {ID: 2, State: active}}, nil, map[uint32]error{1: nil, 2: nil})
		fakeLocks(t, map[uint32]bool{1: true, 2: false})
		fakeTray(t, b, 2, `{"ok":true,"audio":{"volume":20,"muted":true,"device":"원격 오디오"}}`)

		res, err := runUserAction(context.Background(), "audio", "mute", "on")
		if err != nil || res.Audio == nil || !res.Audio.Muted {
			t.Fatalf("%+v %v", res, err)
		}
		if s, ok := currentAudio(); !ok || !s.Muted || s.Device != "원격 오디오" {
			t.Errorf("the reply was not stored: %+v %v", s, ok)
		}
		if !trayConnected(sessionActiveUser) || trayConnected(sessionConsole) {
			t.Error("trayConnected: want the RDP session's tray only")
		}
		// No tray on the console: the screen command says so.
		if _, err := runUserActionIn(context.Background(), sessionConsole, "screen", "off"); !errors.Is(err, session.ErrTrayUnavailable) {
			t.Errorf("screen off without a console tray: %v", err)
		}
	})
}
