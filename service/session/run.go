package session

// The service half of the user-session action channel (#103, design
// docs/design/media-notify.md §2). Volume, media keys and toasts only mean
// something in the logged-in user's session, so the service runs its own
// executable there as `user-action …` (Command) and reads its one-line
// JSON reply. The subcommand itself — grammar, validation, output — is
// package useraction.
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
	"time"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

// ErrTimeout is returned when the child did not answer within the runner's
// Timeout (it has been killed by then).
var ErrTimeout = errors.New("user-action timed out")

// ErrOutput is returned when the child's last output line is not a
// user-action reply: nothing at all, or not JSON with an "ok" field.
var ErrOutput = errors.New("user-action: unreadable output")

// ActionError is an {"ok":false} reply. Code is one of the useraction
// codes (bad_args, unsupported, failed).
type ActionError struct {
	Code    string
	Message string
}

func (e *ActionError) Error() string {
	return fmt.Sprintf("user-action %s: %s", e.Code, e.Message)
}

// Result is one parsed reply line.
type Result struct {
	OK      bool              `json:"ok"`
	Error   string            `json:"error,omitempty"`
	Message string            `json:"message,omitempty"`
	Audio   *useraction.Audio `json:"audio,omitempty"`
	// Fields holds every key of the reply, including the ones above, for
	// the results a feature adds without a typed field.
	Fields map[string]json.RawMessage `json:"-"`
}

// NowPlaying reads the "media" object of a `media info` reply. A media key
// reply has a string there ("media":"next") and reads as ok=false, as does
// anything that fails Validate.
func (r Result) NowPlaying() (useraction.NowPlaying, bool) {
	raw := bytes.TrimSpace(r.Fields["media"])
	if len(raw) == 0 || raw[0] != '{' {
		return useraction.NowPlaying{}, false
	}
	var np useraction.NowPlaying
	if json.Unmarshal(raw, &np) != nil || np.Validate() != nil {
		return useraction.NowPlaying{}, false
	}
	return np, true
}

// ReplyString reads a string field of a reply, "" when absent.
func (r Result) ReplyString(key string) string {
	var s string
	if raw, ok := r.Fields[key]; ok {
		_ = json.Unmarshal(raw, &s) // not a string: ""
	}
	return s
}

// Runner runs `<exe> user-action <args…>` in a user session. Every field
// is a dependency the service fills in (and a test replaces).
type Runner struct {
	// Exec runs exe with args in the target session and returns its
	// combined output, also when the process fails. It must stop when ctx
	// is done; the real one (WTS.Output) never touches a shell.
	Exec func(ctx context.Context, target Target, exe string, args []string) ([]byte, error)
	// Exe is the executable to run: os.Executable.
	Exe func() (string, error)
	// Timeout bounds one run end to end: finding the session, starting the
	// child and reading its reply. The child is killed when it runs out.
	Timeout time.Duration
	// BeforeRun is called before an ActiveUser run, so a moved target
	// drops the old session's samples before this reply is stored. nil:
	// nothing.
	BeforeRun func()
	// OnReply receives every {"ok":true} reply of an ActiveUser run, for
	// the stores that follow command results (audio, now playing). nil:
	// nothing.
	OnReply func(args []string, res Result)
}

// Run runs `<exe> user-action <args…>` in target's session and returns its
// reply.
//
// err is nil only for an {"ok":true} reply. Otherwise it is one of:
// ErrNoUserSession (nobody logged in), ErrNoConsoleSession (Console only,
// kept whole: it says whether the console is at the logon screen),
// ErrTimeout, *ActionError (an {"ok":false} reply, whose code is also in
// the returned result), ErrOutput (no reply line), or a start error.
//
// Only an ActiveUser run touches the target bookkeeping (BeforeRun,
// OnReply). A Console run does neither — the console is not "the target"
// just because a screen command ran there, and what it reads would
// describe a session the heartbeat filter may be ignoring.
func (rn Runner) Run(ctx context.Context, target Target, args ...string) (Result, error) {
	if _, err := useraction.Parse(args); err != nil {
		var ue *useraction.Error
		errors.As(err, &ue)
		res := Result{OK: false, Error: ue.Code, Message: ue.Message}
		return res, &ActionError{Code: ue.Code, Message: ue.Message}
	}
	exe, err := rn.Exe()
	if err != nil {
		return Result{}, fmt.Errorf("get executable: %w", err)
	}

	tracksTarget := target == ActiveUser
	if tracksTarget && rn.BeforeRun != nil {
		rn.BeforeRun()
	}

	runCtx, cancel := context.WithTimeout(ctx, rn.Timeout)
	defer cancel()
	out, runErr := rn.Exec(runCtx, target, exe, append([]string{"user-action"}, args...))

	switch {
	case errors.Is(runErr, ErrNoUserSession):
		return Result{}, ErrNoUserSession
	case errors.Is(runErr, ErrNoConsoleSession):
		return Result{}, runErr
	case ctx.Err() != nil:
		// The caller gave up (service stopping, request gone).
		return Result{}, ctx.Err()
	case runCtx.Err() != nil:
		return Result{}, fmt.Errorf("%w after %v (user-action %s)", ErrTimeout, rn.Timeout, args[0])
	}

	res, perr := ParseOutput(out)
	if perr != nil {
		if runErr != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrOutput, runErr)
		}
		return Result{}, perr
	}
	if !res.OK {
		return res, &ActionError{Code: res.Error, Message: res.Message}
	}
	if tracksTarget && rn.OnReply != nil {
		rn.OnReply(args, res)
	}
	return res, nil
}

// ParseOutput reads the reply from a child's combined output: the last
// non-empty line, which must be a JSON object with a boolean "ok". Anything
// before it — a runtime warning, a stray line some DLL printed — is
// ignored. An {"ok":false} reply without a code gets "failed".
func ParseOutput(out []byte) (Result, error) {
	line := lastNonEmptyLine(out)
	if len(line) == 0 {
		return Result{}, fmt.Errorf("%w: empty", ErrOutput)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return Result{}, fmt.Errorf("%w: %v in %q", ErrOutput, err, clip(line, 200))
	}
	var res Result
	if err := json.Unmarshal(line, &res); err != nil {
		return Result{}, fmt.Errorf("%w: %v in %q", ErrOutput, err, clip(line, 200))
	}
	// Exactly true or false: json.Unmarshal would also take null as false.
	if raw := string(bytes.TrimSpace(fields["ok"])); raw != "true" && raw != "false" {
		return Result{}, fmt.Errorf("%w: no boolean \"ok\" in %q", ErrOutput, clip(line, 200))
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
