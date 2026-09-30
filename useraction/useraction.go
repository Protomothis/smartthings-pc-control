// Package useraction is the `user-action` subcommand (#103): the half of
// the user-session action channel that runs inside the logged-in user's
// session. The service lives in session 0, where the default audio
// endpoint, media keys, toasts and speech mean nothing, so it launches this
// same executable in the user's session (CreateProcessAsUser) with a fixed
// argument vector and reads back one line of JSON.
//
// Grammar (exact strings, docs/design/media-notify.md §2):
//
//	user-action audio get
//	user-action audio set <0-100>
//	user-action audio step <-100..100>
//	user-action audio mute <on|off|toggle>
//	user-action media <playpause|play|pause|stop|next|prev>
//	user-action media info
//	user-action notify --title <t> --text <t> [--speak] [--voice <name>]
//	user-action preset --type <program|url|script> --path <p> [--arg <a>]...
//	user-action speak --text <t> [--voice <name>]
//
// speak is internal to notify (#106): the service never builds it. A
// notify with --speak shows its toast, answers at once and leaves the
// reading to a detached `speak` child, because reading 200 characters aloud
// takes far longer than the service's 3 s budget for one user-action run.
//
// Output is exactly one line on stdout, and the exit code follows it:
//
//	{"ok":true,...}                                       exit 0
//	{"ok":false,"error":"<code>","message":"..."}         exit 1
//
// with code one of bad_args, unsupported, failed.
//
// This package owns the parsing, the validation and the output; the actual
// work is done by handlers that the feature issues register (audio #104,
// media #105, notify #106, preset #109). An action nobody has registered
// answers "unsupported", which is what every action does until then.
package useraction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Error codes of an {"ok":false} line.
const (
	CodeBadArgs     = "bad_args"
	CodeUnsupported = "unsupported"
	CodeFailed      = "failed"
)

// Actions a handler can be registered for.
const (
	ActionAudio  = "audio"
	ActionMedia  = "media"
	ActionNotify = "notify"
	ActionPreset = "preset"
	// ActionSpeak is the detached reader a notify --speak starts (#106).
	ActionSpeak = "speak"
)

// Validation limits. The service applies its own (stricter or equal)
// limits before it builds the argument vector; these keep the subcommand
// safe on its own.
const (
	MaxTitleRunes  = 100
	MaxTextRunes   = 200
	MaxVoiceRunes  = 128
	MaxPathRunes   = 1024
	MaxArgRunes    = 1024
	MaxPresetArgs  = 32
	MaxDeviceRunes = 128
)

// Audio is the state of the default playback device. It is the "audio"
// object of a user-action result and of the tray heartbeat.
type Audio struct {
	Volume int    `json:"volume"`
	Muted  bool   `json:"muted"`
	Device string `json:"device"`
}

// Validate checks the ranges a sample must be in to be stored.
func (a Audio) Validate() error {
	if a.Volume < 0 || a.Volume > 100 {
		return fmt.Errorf("volume %d out of range 0-100", a.Volume)
	}
	if utf8.RuneCountInString(a.Device) > MaxDeviceRunes {
		return fmt.Errorf("device name longer than %d characters", MaxDeviceRunes)
	}
	if hasControl(a.Device) {
		return errors.New("device name contains control characters")
	}
	return nil
}

// Request is one parsed, validated invocation. Only the fields of its
// Action are set.
type Request struct {
	Action string // ActionAudio, ActionMedia, ActionNotify or ActionPreset

	// Verb is the audio verb (get, set, step, mute), the media key
	// (playpause, play, pause, stop, next, prev) or MediaInfo.
	Verb string
	// Value is the audio set level (0..100) or step (-100..100).
	Value int
	// Mode is the audio mute mode: on, off or toggle.
	Mode string

	// notify
	Title string
	Text  string
	Speak bool
	Voice string

	// preset
	PresetType string // program, url or script
	Path       string
	Args       []string
}

// Handler performs one action in the user session. The returned fields are
// merged into the {"ok":true,...} line (an "ok" key is ignored). A returned
// *Error keeps its code; any other error is reported as "failed".
type Handler func(req Request) (map[string]any, error)

var (
	handlersMu sync.RWMutex
	handlers   = map[string]Handler{}
)

// Register installs the handler for one action (ActionAudio, ActionMedia,
// ActionNotify, ActionPreset). It is meant to be called from an init
// function and panics on an unknown action or a second registration, both
// of which are programming errors.
func Register(action string, h Handler) {
	if !knownAction(action) {
		panic("useraction: Register of unknown action " + strconv.Quote(action))
	}
	if h == nil {
		panic("useraction: Register of nil handler for " + action)
	}
	handlersMu.Lock()
	defer handlersMu.Unlock()
	if _, dup := handlers[action]; dup {
		panic("useraction: second Register for " + action)
	}
	handlers[action] = h
}

// lookup returns the registered handler, or nil.
func lookup(action string) Handler {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	return handlers[action]
}

func knownAction(action string) bool {
	switch action {
	case ActionAudio, ActionMedia, ActionNotify, ActionPreset, ActionSpeak:
		return true
	}
	return false
}

// Error is an {"ok":false} outcome with its code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func badArgs(format string, args ...any) *Error {
	return &Error{Code: CodeBadArgs, Message: fmt.Sprintf(format, args...)}
}

// Unsupported is the error a handler returns when this machine cannot do
// the action (no audio device, no speech engine, ...).
func Unsupported(format string, args ...any) error {
	return &Error{Code: CodeUnsupported, Message: fmt.Sprintf(format, args...)}
}

// Failed is the error a handler returns when the action was attempted and
// did not work.
func Failed(format string, args ...any) error {
	return &Error{Code: CodeFailed, Message: fmt.Sprintf(format, args...)}
}

// Parse validates the arguments that follow "user-action". The error is
// always a *Error with CodeBadArgs.
func Parse(args []string) (Request, error) {
	if len(args) == 0 {
		return Request{}, badArgs("missing action")
	}
	for _, a := range args {
		if !utf8.ValidString(a) {
			return Request{}, badArgs("argument is not valid UTF-8")
		}
	}
	switch action, rest := args[0], args[1:]; action {
	case ActionAudio:
		return parseAudio(rest)
	case ActionMedia:
		return parseMedia(rest)
	case ActionNotify:
		return parseNotify(rest)
	case ActionPreset:
		return parsePreset(rest)
	case ActionSpeak:
		return parseSpeak(rest)
	default:
		return Request{}, badArgs("unknown action %q", action)
	}
}

func parseAudio(args []string) (Request, error) {
	req := Request{Action: ActionAudio}
	if len(args) == 0 {
		return req, badArgs("audio: missing verb (get, set, step, mute)")
	}
	req.Verb = args[0]
	switch req.Verb {
	case "get":
		if len(args) != 1 {
			return req, badArgs("audio get takes no arguments")
		}
	case "set":
		if len(args) != 2 {
			return req, badArgs("usage: audio set <0-100>")
		}
		n, ok := parseInt(args[1], false)
		if !ok || n < 0 || n > 100 {
			return req, badArgs("audio set: %q is not a level 0-100", args[1])
		}
		req.Value = n
	case "step":
		if len(args) != 2 {
			return req, badArgs("usage: audio step <-100..100>")
		}
		n, ok := parseInt(args[1], true)
		if !ok || n < -100 || n > 100 {
			return req, badArgs("audio step: %q is not a step -100..100", args[1])
		}
		req.Value = n
	case "mute":
		if len(args) != 2 {
			return req, badArgs("usage: audio mute <on|off|toggle>")
		}
		switch args[1] {
		case "on", "off", "toggle":
			req.Mode = args[1]
		default:
			return req, badArgs("audio mute: %q is not on, off or toggle", args[1])
		}
	default:
		return req, badArgs("audio: unknown verb %q", req.Verb)
	}
	return req, nil
}

// parseInt accepts plain decimal digits, with a leading sign only when
// signed is set. strconv.Atoi alone would also take "+5" for a level and
// leading zeros of any length; neither matters, but a narrow grammar keeps
// the service's argument vector and this parser in exact agreement.
func parseInt(s string, signed bool) (int, bool) {
	digits := s
	if signed && (strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-")) {
		digits = s[1:]
	}
	if digits == "" || len(digits) > 3 {
		return 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// MediaKeys are the media verbs, in the order of the grammar.
var MediaKeys = []string{"playpause", "play", "pause", "stop", "next", "prev"}

func parseMedia(args []string) (Request, error) {
	req := Request{Action: ActionMedia}
	if len(args) != 1 {
		return req, badArgs("usage: media <%s|%s>", strings.Join(MediaKeys, "|"), MediaInfo)
	}
	if args[0] == MediaInfo {
		req.Verb = MediaInfo
		return req, nil
	}
	for _, k := range MediaKeys {
		if args[0] == k {
			req.Verb = k
			return req, nil
		}
	}
	return req, badArgs("media: unknown key %q", args[0])
}

// flagSet reads "--name value" and bare "--name" flags. A value is always
// the next argument taken literally, so user text that happens to start
// with "--" is still text; "--name=value" is not accepted.
type flagSet struct {
	valued map[string]bool // flag → takes a value
	repeat map[string]bool // flag → may repeat
}

func (f flagSet) parse(args []string) (map[string][]string, error) {
	out := map[string][]string{}
	for i := 0; i < len(args); i++ {
		name := args[i]
		valued, known := f.valued[name]
		if !known {
			return nil, badArgs("unexpected argument %q", name)
		}
		if _, seen := out[name]; seen && !f.repeat[name] {
			return nil, badArgs("%s given twice", name)
		}
		if !valued {
			out[name] = append(out[name], "")
			continue
		}
		if i+1 >= len(args) {
			return nil, badArgs("%s needs a value", name)
		}
		i++
		out[name] = append(out[name], args[i])
	}
	return out, nil
}

func parseNotify(args []string) (Request, error) {
	req := Request{Action: ActionNotify}
	flags, err := flagSet{
		valued: map[string]bool{"--title": true, "--text": true, "--speak": false, "--voice": true},
	}.parse(args)
	if err != nil {
		return req, err
	}
	title, hasTitle := flags["--title"]
	text, hasText := flags["--text"]
	if !hasTitle || !hasText {
		return req, badArgs("usage: notify --title <t> --text <t> [--speak] [--voice <name>]")
	}
	req.Title, req.Text = title[0], text[0]
	_, req.Speak = flags["--speak"]
	if v, ok := flags["--voice"]; ok {
		req.Voice = v[0]
		if strings.TrimSpace(req.Voice) == "" {
			return req, badArgs("notify: --voice is empty")
		}
	}
	if strings.TrimSpace(req.Text) == "" {
		return req, badArgs("notify: --text is empty")
	}
	for _, c := range []struct {
		name, v string
		max     int
	}{{"--title", req.Title, MaxTitleRunes}, {"--text", req.Text, MaxTextRunes}, {"--voice", req.Voice, MaxVoiceRunes}} {
		if n := utf8.RuneCountInString(c.v); n > c.max {
			return req, badArgs("notify: %s longer than %d characters", c.name, c.max)
		}
		// The service strips control characters before it gets here
		// (§7); one arriving anyway is a caller bug, not text to show.
		if hasControl(c.v) {
			return req, badArgs("notify: %s contains control characters", c.name)
		}
	}
	return req, nil
}

// parseSpeak reads `speak --text <t> [--voice <name>]` with the limits of
// notify.
func parseSpeak(args []string) (Request, error) {
	req := Request{Action: ActionSpeak}
	flags, err := flagSet{
		valued: map[string]bool{"--text": true, "--voice": true},
	}.parse(args)
	if err != nil {
		return req, err
	}
	text, ok := flags["--text"]
	if !ok {
		return req, badArgs("usage: speak --text <t> [--voice <name>]")
	}
	req.Text = text[0]
	if v, ok := flags["--voice"]; ok {
		req.Voice = v[0]
		if strings.TrimSpace(req.Voice) == "" {
			return req, badArgs("speak: --voice is empty")
		}
	}
	if strings.TrimSpace(req.Text) == "" {
		return req, badArgs("speak: --text is empty")
	}
	if utf8.RuneCountInString(req.Text) > MaxTextRunes || hasControl(req.Text) {
		return req, badArgs("speak: --text is too long or contains control characters")
	}
	if utf8.RuneCountInString(req.Voice) > MaxVoiceRunes || hasControl(req.Voice) {
		return req, badArgs("speak: --voice is too long or contains control characters")
	}
	return req, nil
}

// scriptExts are the script types a preset may run (§10), each with a
// fixed interpreter chosen by the preset handler.
var scriptExts = map[string]bool{".ps1": true, ".bat": true, ".cmd": true}

func parsePreset(args []string) (Request, error) {
	req := Request{Action: ActionPreset}
	flags, err := flagSet{
		valued: map[string]bool{"--type": true, "--path": true, "--arg": true},
		repeat: map[string]bool{"--arg": true},
	}.parse(args)
	if err != nil {
		return req, err
	}
	typ, hasType := flags["--type"]
	path, hasPath := flags["--path"]
	if !hasType || !hasPath {
		return req, badArgs("usage: preset --type <program|url|script> --path <p> [--arg <a>]...")
	}
	req.PresetType, req.Path, req.Args = typ[0], path[0], flags["--arg"]
	if strings.TrimSpace(req.Path) == "" {
		return req, badArgs("preset: --path is empty")
	}
	if utf8.RuneCountInString(req.Path) > MaxPathRunes || hasControl(req.Path) {
		return req, badArgs("preset: --path is too long or contains control characters")
	}
	if len(req.Args) > MaxPresetArgs {
		return req, badArgs("preset: more than %d --arg", MaxPresetArgs)
	}
	for _, a := range req.Args {
		if utf8.RuneCountInString(a) > MaxArgRunes || hasControl(a) {
			return req, badArgs("preset: --arg is too long or contains control characters")
		}
	}
	switch req.PresetType {
	case "program":
	case "script":
		if !scriptExts[strings.ToLower(filepath.Ext(req.Path))] {
			return req, badArgs("preset: script must be a .ps1, .bat or .cmd file")
		}
	case "url":
		if len(req.Args) > 0 {
			return req, badArgs("preset: a url takes no --arg")
		}
		u, err := url.Parse(req.Path)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return req, badArgs("preset: url must be http:// or https://")
		}
	default:
		return req, badArgs("preset: unknown --type %q", req.PresetType)
	}
	return req, nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// Main runs the subcommand: args are what follows "user-action". It writes
// exactly one JSON line to stdout and returns the process exit code.
func Main(args []string, stdout io.Writer) int {
	fields, err := dispatch(args)
	line, code := render(fields, err)
	stdout.Write(line)
	return code
}

// dispatch parses the arguments and calls the registered handler. A panic
// in a handler becomes a "failed" result so the one-line contract holds
// whatever the handler does.
func dispatch(args []string) (fields map[string]any, err error) {
	req, err := Parse(args)
	if err != nil {
		return nil, err
	}
	h := lookup(req.Action)
	if h == nil {
		return nil, &Error{Code: CodeUnsupported, Message: req.Action + " is not available in this build"}
	}
	defer func() {
		if r := recover(); r != nil {
			fields, err = nil, Failed("%s: panic: %v", req.Action, r)
		}
	}()
	return h(req)
}

// render turns an outcome into its line (with the trailing newline) and
// exit code. "ok" is always the first key so the line reads naturally in
// a log; the remaining keys are sorted as encoding/json sorts map keys.
func render(fields map[string]any, err error) ([]byte, int) {
	if err != nil {
		var ue *Error
		if !errors.As(err, &ue) {
			ue = &Error{Code: CodeFailed, Message: err.Error()}
		}
		b, _ := json.Marshal(struct {
			OK      bool   `json:"ok"`
			Error   string `json:"error"`
			Message string `json:"message"`
		}{false, ue.Code, ue.Message})
		return append(b, '\n'), 1
	}
	var buf bytes.Buffer
	buf.WriteString(`{"ok":true`)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k != "ok" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		kb, _ := json.Marshal(k)
		vb, merr := json.Marshal(fields[k])
		if merr != nil {
			return render(nil, Failed("result field %s: %v", k, merr))
		}
		buf.WriteByte(',')
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteString("}\n")
	return buf.Bytes(), 0
}
