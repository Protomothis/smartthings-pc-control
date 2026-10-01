package service

// Tests for the service half of the user-action channel (#103): the reply
// parser, runUserAction over an injected runner, the heartbeat audio block
// and the audio store.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/session"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

func TestParseUserActionOutput(t *testing.T) {
	ok := []struct {
		name string
		out  string
		want UserActionResult
	}{
		{"plain", `{"ok":true}` + "\n", UserActionResult{OK: true}},
		{"no newline", `{"ok":true}`, UserActionResult{OK: true}},
		{"audio", `{"ok":true,"audio":{"volume":30,"muted":false,"device":"스피커"}}` + "\n",
			UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 30, Device: "스피커"}}},
		{"noise before", "WARNING: something\r\nanother line\n\n" + `{"ok":true,"audio":{"volume":5,"muted":true,"device":""}}` + "\r\n\r\n",
			UserActionResult{OK: true, Audio: &useraction.Audio{Volume: 5, Muted: true}}},
		{"error reply", `{"ok":false,"error":"unsupported","message":"no device"}` + "\n",
			UserActionResult{OK: false, Error: "unsupported", Message: "no device"}},
		{"error without code", `{"ok":false}`, UserActionResult{OK: false, Error: "failed"}},
		// Only the last line counts, even when an earlier one looks like a
		// reply.
		{"last line wins", `{"ok":false,"error":"failed"}` + "\n" + `{"ok":true}` + "\n", UserActionResult{OK: true}},
	}
	for _, c := range ok {
		got, err := session.ParseOutput([]byte(c.out))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		got.Fields = nil
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}

	res, err := session.ParseOutput([]byte(`{"ok":true,"presets":[1,2]}`))
	if err != nil || string(res.Fields["presets"]) != "[1,2]" {
		t.Errorf("Fields = %v, %v; want the untyped keys kept", res.Fields, err)
	}

	for name, out := range map[string]string{
		"empty":        "",
		"blank lines":  "\r\n\n  \n",
		"not json":     "panic: runtime error\n",
		"json at top":  `{"ok":true}` + "\nexit status 2\n",
		"array":        `[1,2]`,
		"no ok":        `{"error":"failed"}`,
		"ok not bool":  `{"ok":"yes"}`,
		"ok null":      `{"ok":null}`,
		"bad audio":    `{"ok":true,"audio":"loud"}`,
		"truncated":    `{"ok":tr`,
		"string value": `"ok"`,
	} {
		if _, err := session.ParseOutput([]byte(out)); !errors.Is(err, errUserActionOutput) {
			t.Errorf("%s: err = %v, want errUserActionOutput", name, err)
		}
	}
}

// fakeUserAction installs a runner and restores the real one afterwards.
func fakeUserAction(t *testing.T, run func(ctx context.Context, exe string, args []string) ([]byte, error)) {
	t.Helper()
	saved := userActions
	userActions.Exec = func(ctx context.Context, _ sessionTarget, exe string, args []string) ([]byte, error) {
		return run(ctx, exe, args)
	}
	userActions.Exe = func() (string, error) { return `C:\PC Control\SmartThingsPCControl.exe`, nil }
	resetAudioSample()
	t.Cleanup(func() {
		userActions = saved
		resetAudioSample()
		clock.audio = time.Now
	})
}

func TestRunUserActionOK(t *testing.T) {
	var gotExe string
	var gotArgs []string
	fakeUserAction(t, func(ctx context.Context, exe string, args []string) ([]byte, error) {
		gotExe, gotArgs = exe, args
		if _, has := ctx.Deadline(); !has {
			t.Error("runner context has no deadline")
		}
		return []byte("noise\n" + `{"ok":true,"audio":{"volume":42,"muted":true,"device":"헤드폰"}}` + "\n"), nil
	})
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	clock.audio = func() time.Time { return at }

	res, err := runUserAction(context.Background(), "audio", "set", "42")
	if err != nil {
		t.Fatalf("runUserAction: %v", err)
	}
	if gotExe != `C:\PC Control\SmartThingsPCControl.exe` {
		t.Errorf("exe = %q", gotExe)
	}
	if want := []string{"user-action", "audio", "set", "42"}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %q, want %q", gotArgs, want)
	}
	if !res.OK || res.Audio == nil || res.Audio.Volume != 42 {
		t.Errorf("result = %+v", res)
	}
	// The reply's audio updates the store.
	s, ok := currentAudio()
	if !ok || s.Volume != 42 || !s.Muted || s.Device != "헤드폰" || !s.UpdatedAt.Equal(at) {
		t.Errorf("currentAudio = %+v, %v", s, ok)
	}
}

func TestRunUserActionArgsAreNotShellParsed(t *testing.T) {
	var gotArgs []string
	fakeUserAction(t, func(ctx context.Context, exe string, args []string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"ok":true}`), nil
	})
	text := `a & del C:\x | "quoted" %PATH% $(x)`
	if _, err := runUserAction(context.Background(), "notify", "--title", "t", "--text", text); err != nil {
		t.Fatal(err)
	}
	if want := []string{"user-action", "notify", "--title", "t", "--text", text}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %q, want them passed through untouched", gotArgs)
	}
}

func TestRunUserActionBadArgsNotLaunched(t *testing.T) {
	launched := false
	fakeUserAction(t, func(context.Context, string, []string) ([]byte, error) {
		launched = true
		return nil, nil
	})
	res, err := runUserAction(context.Background(), "audio", "set", "250")
	var ue *userActionError
	if !errors.As(err, &ue) || ue.Code != useraction.CodeBadArgs || res.Error != useraction.CodeBadArgs {
		t.Errorf("err = %v, result %+v; want bad_args", err, res)
	}
	if launched {
		t.Error("a request the parser rejects was launched")
	}
	if _, err := runUserAction(context.Background()); !errors.As(err, &ue) || ue.Code != useraction.CodeBadArgs {
		t.Errorf("no args: err = %v, want bad_args", err)
	}
}

func TestRunUserActionErrorReply(t *testing.T) {
	fakeUserAction(t, func(context.Context, string, []string) ([]byte, error) {
		// The child exits 1 after an {"ok":false} line; the reply still
		// decides what happened.
		return []byte(`{"ok":false,"error":"unsupported","message":"audio is not available in this build"}` + "\n"),
			fmt.Errorf("exec error: %w", errors.New("exit status 1"))
	})
	res, err := runUserAction(context.Background(), "audio", "get")
	var ue *userActionError
	if !errors.As(err, &ue) || ue.Code != "unsupported" || ue.Message == "" {
		t.Errorf("err = %v, want unsupported", err)
	}
	if res.OK || res.Error != "unsupported" {
		t.Errorf("result = %+v", res)
	}
}

func TestRunUserActionUnreadable(t *testing.T) {
	for name, run := range map[string]func(context.Context, string, []string) ([]byte, error){
		"empty, exit 0": func(context.Context, string, []string) ([]byte, error) { return nil, nil },
		"garbage, exit 2": func(context.Context, string, []string) ([]byte, error) {
			return []byte("panic: boom\ngoroutine 1\n"), errors.New("exit status 2")
		},
	} {
		fakeUserAction(t, run)
		if _, err := runUserAction(context.Background(), "media", "next"); !errors.Is(err, errUserActionOutput) {
			t.Errorf("%s: err = %v, want errUserActionOutput", name, err)
		}
	}
}

func TestRunUserActionNoSession(t *testing.T) {
	fakeUserAction(t, func(context.Context, string, []string) ([]byte, error) {
		return nil, fmt.Errorf("get session: %w", fmt.Errorf("%w: no explorer.exe process found", errNoUserSession))
	})
	if _, err := runUserAction(context.Background(), "media", "play"); !errors.Is(err, errNoUserSession) {
		t.Errorf("err = %v, want errNoUserSession", err)
	}
}

func TestRunUserActionTimeout(t *testing.T) {
	killed := make(chan struct{})
	fakeUserAction(t, func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		// Behave like exec.CommandContext: run until the context kills us.
		<-ctx.Done()
		close(killed)
		return []byte("partial"), errors.New("exec error: exit status 1")
	})
	userActions.Timeout = 50 * time.Millisecond

	start := time.Now()
	_, err := runUserAction(context.Background(), "audio", "get")
	if !errors.Is(err, errUserActionTimeout) {
		t.Errorf("err = %v, want errUserActionTimeout", err)
	}
	select {
	case <-killed:
	default:
		t.Error("the runner's context was not cancelled")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v, the timeout did not apply", d)
	}
	if _, ok := currentAudio(); ok {
		t.Error("a timed-out run stored audio")
	}
}

func TestRunUserActionTimeoutDefault(t *testing.T) {
	if userActions.Timeout != 3*time.Second {
		t.Errorf("userActions.Timeout = %v, want 3s (§2)", userActions.Timeout)
	}
}

func TestRunUserActionCallerCancel(t *testing.T) {
	fakeUserAction(t, func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runUserAction(ctx, "media", "stop"); !errors.Is(err, context.Canceled) || errors.Is(err, errUserActionTimeout) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRunUserActionInvalidAudioIgnored(t *testing.T) {
	fakeUserAction(t, func(context.Context, string, []string) ([]byte, error) {
		return []byte(`{"ok":true,"audio":{"volume":180,"muted":false,"device":"x"}}`), nil
	})
	if _, err := runUserAction(context.Background(), "audio", "get"); err != nil {
		t.Fatal(err)
	}
	if _, ok := currentAudio(); ok {
		t.Error("an out-of-range audio reply was stored")
	}
}

func TestSessionHeartbeatAudio(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t"})
	idleSetup(t)
	resetAudioSample()
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	clock.audio = func() time.Time { return at }
	t.Cleanup(func() { resetAudioSample(); clock.audio = time.Now })

	// Without audio: the idle sample is stored, the audio store untouched.
	if w := heartbeatDo(t, `{"idle_seconds":12}`, true, true); w.Code != http.StatusOK {
		t.Fatalf("heartbeat without audio: %d (%s)", w.Code, w.Body.String())
	}
	if _, ok := currentAudio(); ok {
		t.Error("a heartbeat without audio stored an audio sample")
	}

	// With audio.
	if w := heartbeatDo(t, `{"idle_seconds":13,"audio":{"volume":30,"muted":false,"device":"스피커"}}`, true, true); w.Code != http.StatusOK {
		t.Fatalf("heartbeat with audio: %d (%s)", w.Code, w.Body.String())
	}
	s, ok := currentAudio()
	if !ok || s.Volume != 30 || s.Muted || s.Device != "스피커" || !s.UpdatedAt.Equal(at) {
		t.Errorf("currentAudio = %+v, %v", s, ok)
	}
	if idle, _ := lastIdleSeconds(); idle != 13 {
		t.Errorf("idle = %d, want 13", idle)
	}

	// Audio alone (idle not exposed) leaves the idle sample alone.
	at = at.Add(30 * time.Second)
	if w := heartbeatDo(t, `{"audio":{"volume":31,"muted":true,"device":"스피커"}}`, true, true); w.Code != http.StatusOK {
		t.Fatalf("audio-only heartbeat: %d (%s)", w.Code, w.Body.String())
	}
	if s, _ := currentAudio(); s.Volume != 31 || !s.Muted {
		t.Errorf("currentAudio = %+v after the audio-only heartbeat", s)
	}
	if idle, ok := lastIdleSeconds(); !ok || idle != 13 {
		t.Errorf("idle = %d, %v; an audio-only heartbeat changed it", idle, ok)
	}

	// A bad audio block rejects the whole body: neither sample changes.
	for _, body := range []string{
		`{"idle_seconds":99,"audio":{"volume":101,"muted":false,"device":""}}`,
		`{"idle_seconds":99,"audio":{"volume":-1,"muted":false,"device":""}}`,
		`{"idle_seconds":99,"audio":{"volume":"30"}}`,
		`{"idle_seconds":99,"audio":{"volume":30,"muted":false,"device":"a\nb"}}`,
	} {
		if w := heartbeatDo(t, body, true, true); w.Code != http.StatusBadRequest {
			t.Errorf("body %s: %d, want 400", body, w.Code)
		}
	}
	if s, _ := currentAudio(); s.Volume != 31 {
		t.Errorf("a rejected body changed the audio sample (%+v)", s)
	}
	if idle, _ := lastIdleSeconds(); idle != 13 {
		t.Errorf("a rejected body changed the idle sample (%d)", idle)
	}
}

func TestSessionHeartbeatAudioNeedsAuth(t *testing.T) {
	stSetup(t, Config{Port: 5001, Secret: "s3cr3t"})
	idleSetup(t)
	resetAudioSample()
	t.Cleanup(resetAudioSample)
	if w := heartbeatDo(t, `{"audio":{"volume":30,"muted":false,"device":"x"}}`, false, true); w.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d, want 401", w.Code)
	}
	if _, ok := currentAudio(); ok {
		t.Error("an unauthenticated heartbeat stored audio")
	}
}
