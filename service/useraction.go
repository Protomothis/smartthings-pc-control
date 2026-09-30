package service

// The service half of the user-session action channel (#103, design
// docs/design/media-notify.md §2). Volume, media keys, toasts and speech
// only mean something in the logged-in user's session, so the service runs
// this same executable there as `user-action …` (CreateProcessAsUser via
// userSessionCommand) and reads its one-line JSON reply. The subcommand
// itself — grammar, validation, output — is package useraction.
//
// The argument vector goes to CreateProcessAsUser as-is; no shell ever sees
// it (§7). It is validated with the subcommand's own parser before anything
// is launched, so a bad request costs no process.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// userActionTimeout bounds one user-action run end to end: finding the
// session, starting the child and reading its reply. The child is killed
// when it runs out. A var so the tests can shorten it.
var userActionTimeout = 3 * time.Second

// errUserActionTimeout is returned when the child did not answer within
// userActionTimeout (it has been killed by then).
var errUserActionTimeout = errors.New("user-action timed out")

// errUserActionOutput is returned when the child's last output line is not
// a user-action reply: nothing at all, or not JSON with an "ok" field.
var errUserActionOutput = errors.New("user-action: unreadable output")

// userActionError is an {"ok":false} reply. Code is one of the useraction
// codes (bad_args, unsupported, failed).
type userActionError struct {
	Code    string
	Message string
}

func (e *userActionError) Error() string {
	return fmt.Sprintf("user-action %s: %s", e.Code, e.Message)
}

// UserActionResult is one parsed reply line.
type UserActionResult struct {
	OK      bool              `json:"ok"`
	Error   string            `json:"error,omitempty"`
	Message string            `json:"message,omitempty"`
	Audio   *useraction.Audio `json:"audio,omitempty"`
	// Fields holds every key of the reply, including the ones above, for
	// the results a feature adds without a typed field.
	Fields map[string]json.RawMessage `json:"-"`
}

// userActionExec runs the executable with the given arguments in the user
// session and returns its combined output; the output is returned even
// when the process fails. It must stop when ctx is done. The tests replace
// it; the real one never touches a shell.
var userActionExec = func(ctx context.Context, exe string, args []string) ([]byte, error) {
	return outputInUserSession(ctx, exe, args...)
}

// userActionExe is os.Executable, replaced by the tests.
var userActionExe = os.Executable

// runUserAction runs `<this exe> user-action <args…>` in the interactive
// user's session and returns its reply.
//
// err is nil only for an {"ok":true} reply. Otherwise it is one of:
// errNoUserSession (nobody logged in), errUserActionTimeout,
// *userActionError (an {"ok":false} reply, whose code is also in the
// returned result), errUserActionOutput (no reply line), or a start error.
//
// A reply carrying "audio" also updates the audio store, so a command's
// result is visible in status before the next tray heartbeat; a `media
// info` reply updates the now-playing store the same way (#117).
func runUserAction(ctx context.Context, args ...string) (UserActionResult, error) {
	if _, err := useraction.Parse(args); err != nil {
		var ue *useraction.Error
		errors.As(err, &ue)
		res := UserActionResult{OK: false, Error: ue.Code, Message: ue.Message}
		return res, &userActionError{Code: ue.Code, Message: ue.Message}
	}
	exe, err := userActionExe()
	if err != nil {
		return UserActionResult{}, fmt.Errorf("get executable: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, userActionTimeout)
	defer cancel()
	out, runErr := userActionExec(runCtx, exe, append([]string{"user-action"}, args...))

	switch {
	case errors.Is(runErr, errNoUserSession):
		return UserActionResult{}, errNoUserSession
	case ctx.Err() != nil:
		// The caller gave up (service stopping, request gone).
		return UserActionResult{}, ctx.Err()
	case runCtx.Err() != nil:
		return UserActionResult{}, fmt.Errorf("%w after %v (user-action %s)", errUserActionTimeout, userActionTimeout, args[0])
	}

	res, perr := parseUserActionOutput(out)
	if perr != nil {
		if runErr != nil {
			return UserActionResult{}, fmt.Errorf("%w: %v", errUserActionOutput, runErr)
		}
		return UserActionResult{}, perr
	}
	if !res.OK {
		return res, &userActionError{Code: res.Error, Message: res.Message}
	}
	if res.Audio != nil {
		if err := res.Audio.Validate(); err != nil {
			logMsg("user-action %s: ignoring audio in reply: %v", args[0], err)
		} else {
			// Stamped with the completion time: the child read the device
			// just before it answered.
			recordAudioSample(*res.Audio, audioNow())
		}
	}
	if np, ok := res.nowPlaying(); ok {
		recordMediaSample(np, audioNow())
	}
	return res, nil
}

// parseUserActionOutput reads the reply from a child's combined output: the
// last non-empty line, which must be a JSON object with a boolean "ok".
// Anything before it — a runtime warning, a stray line some DLL printed —
// is ignored. An {"ok":false} reply without a code gets "failed".
func parseUserActionOutput(out []byte) (UserActionResult, error) {
	line := lastNonEmptyLine(out)
	if len(line) == 0 {
		return UserActionResult{}, fmt.Errorf("%w: empty", errUserActionOutput)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return UserActionResult{}, fmt.Errorf("%w: %v in %q", errUserActionOutput, err, clip(line, 200))
	}
	var res UserActionResult
	if err := json.Unmarshal(line, &res); err != nil {
		return UserActionResult{}, fmt.Errorf("%w: %v in %q", errUserActionOutput, err, clip(line, 200))
	}
	// Exactly true or false: json.Unmarshal would also take null as false.
	if raw := string(bytes.TrimSpace(fields["ok"])); raw != "true" && raw != "false" {
		return UserActionResult{}, fmt.Errorf("%w: no boolean \"ok\" in %q", errUserActionOutput, clip(line, 200))
	}
	res.Fields = fields
	if !res.OK && res.Error == "" {
		res.Error = useraction.CodeFailed
	}
	return res, nil
}

// lastNonEmptyLine returns the last line of out that is not blank, trimmed.
// Both "\n" and "\r\n" endings occur: the child writes "\n", but a line a
// console-mode DLL printed may use "\r\n".
func lastNonEmptyLine(out []byte) []byte {
	lines := bytes.Split(out, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); len(l) > 0 {
			return l
		}
	}
	return nil
}

func clip(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
