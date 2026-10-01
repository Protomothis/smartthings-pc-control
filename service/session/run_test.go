package session

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeRunner answers every run with out/err and counts the hooks.
func fakeRunner(out string, err error) (*Runner, *int, *int) {
	before, replies := 0, 0
	return &Runner{
		Exec: func(ctx context.Context, _ Target, _ string, _ []string) ([]byte, error) {
			if out == "hang" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return []byte(out), err
		},
		Exe:       func() (string, error) { return `C:\stpc.exe`, nil },
		Timeout:   50 * time.Millisecond,
		BeforeRun: func() { before++ },
		OnReply:   func([]string, Result) { replies++ },
	}, &before, &replies
}

// The target bookkeeping follows ActiveUser runs only.
func TestRunnerHooksFollowTheTarget(t *testing.T) {
	rn, before, replies := fakeRunner(`{"ok":true,"audio":{"volume":30,"muted":false,"device":"x"}}`, nil)
	if _, err := rn.Run(context.Background(), ActiveUser, "audio", "get"); err != nil {
		t.Fatal(err)
	}
	if _, err := rn.Run(context.Background(), Console, "screen", "off"); err != nil {
		t.Fatal(err)
	}
	if *before != 1 || *replies != 1 {
		t.Errorf("BeforeRun %d, OnReply %d; want 1, 1", *before, *replies)
	}
}

func TestRunnerErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := (&Runner{}).Run(ctx, ActiveUser, "no-such-action"); err == nil {
		t.Error("a bad argument vector was run")
	} else if ae := (*ActionError)(nil); !errors.As(err, &ae) || ae.Code != "bad_args" {
		t.Errorf("bad args: %v", err)
	}

	rn, _, _ := fakeRunner("", fmt.Errorf("get session: %w", ErrNoUserSession))
	if _, err := rn.Run(ctx, ActiveUser, "audio", "get"); !errors.Is(err, ErrNoUserSession) {
		t.Errorf("no user: %v", err)
	}
	rn, _, _ = fakeRunner("hang", nil)
	if _, err := rn.Run(ctx, ActiveUser, "audio", "get"); !errors.Is(err, ErrTimeout) {
		t.Errorf("timeout: %v", err)
	}
	rn, _, replies := fakeRunner(`{"ok":false,"error":"unsupported","message":"no device"}`, errors.New("exit status 1"))
	res, err := rn.Run(ctx, ActiveUser, "audio", "get")
	if ae := (*ActionError)(nil); !errors.As(err, &ae) || ae.Code != "unsupported" || res.Message != "no device" || *replies != 0 {
		t.Errorf("refusal: %+v %v (replies %d)", res, err, *replies)
	}
	rn, _, _ = fakeRunner("panic: oops", errors.New("exit status 2"))
	if _, err := rn.Run(ctx, ActiveUser, "audio", "get"); !errors.Is(err, ErrOutput) {
		t.Errorf("unreadable: %v", err)
	}
}

func TestParseOutputAndFields(t *testing.T) {
	res, err := ParseOutput([]byte("warning from a DLL\r\n{\"ok\":true,\"via\":\"session\",\"media\":{\"status\":\"playing\",\"title\":\"Hype Boy\"}}\n\n"))
	if err != nil || !res.OK || res.ReplyString("via") != "session" || res.ReplyString("missing") != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if np, ok := res.NowPlaying(); !ok || np.Status != "playing" || np.Title != "Hype Boy" {
		t.Errorf("now playing %+v %v", np, ok)
	}
	if res, err := ParseOutput([]byte(`{"ok":false}`)); err != nil || res.Error != "failed" {
		t.Errorf("ok:false without a code: %+v %v", res, err)
	}
	for _, bad := range []string{"", "   \n", `{"ok":null}`, `[1]`, `not json`} {
		if _, err := ParseOutput([]byte(bad)); !errors.Is(err, ErrOutput) {
			t.Errorf("ParseOutput(%q) = %v", bad, err)
		}
	}
}
