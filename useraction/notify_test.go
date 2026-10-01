package useraction

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Protomothis/smartthings-pc-control/internal/appid"
)

func TestToastXMLEscapesEverything(t *testing.T) {
	title := `<b>"제목"</b> & more`
	text := `a]]><x/> $(Start-Process calc) ` + "`" + `"@ 'q'`
	got := toastXML(title, text)
	for _, raw := range []string{"<b>", "<x/>", `"제목"`, " & "} {
		if strings.Contains(got, raw) {
			t.Errorf("toast XML contains unescaped %q: %s", raw, got)
		}
	}
	for _, esc := range []string{"&lt;b&gt;", "&#34;제목&#34;", "&amp; more", "]]&gt;&lt;x/&gt;", "$(Start-Process calc)"} {
		if !strings.Contains(got, esc) {
			t.Errorf("toast XML lacks %q: %s", esc, got)
		}
	}
	// No way to act on the toast: no launch argument, no buttons (§7).
	for _, forbidden := range []string{"launch=", "<actions", "<action ", "activationType"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("toast XML has %q: %s", forbidden, got)
		}
	}
	if strings.Count(toastXML("", "x"), "<text>") != 1 {
		t.Error("an empty title should add no text line")
	}
}

func TestToastCommandKeepsTextOutOfTheCommandLine(t *testing.T) {
	text := "$(calc) 빨래 끝"
	cmd := toastCommand("SmartThings", text)
	for _, a := range cmd.Args {
		if strings.Contains(a, "calc") || strings.Contains(a, "빨래") {
			t.Fatalf("argument %q carries the notification text", a)
		}
	}
	if !strings.HasSuffix(strings.ToLower(cmd.Path), `\system32\windowspowershell\v1.0\powershell.exe`) {
		t.Errorf("PowerShell is not started by absolute path: %s", cmd.Path)
	}
	var xmlVar, appVar string
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, toastXMLEnv+"="); ok {
			xmlVar = v
		}
		if v, ok := strings.CutPrefix(kv, toastAppIDEnv+"="); ok {
			appVar = v
		}
	}
	if xmlVar != toastXML("SmartThings", text) || appVar != ToastAppID {
		t.Errorf("env: xml %q, app %q", xmlVar, appVar)
	}
	// The encoded script is the constant one.
	i := len(cmd.Args) - 1
	if cmd.Args[i-1] != "-EncodedCommand" || decodeCommand(t, cmd.Args[i]) != toastScript {
		t.Errorf("encoded command is not toastScript: %q", cmd.Args[i-1:])
	}
}

func decodeCommand(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%2 != 0 {
		t.Fatalf("bad -EncodedCommand %q: %v", s, err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func TestEncodedCommandRoundTrip(t *testing.T) {
	for _, s := range []string{"", "Write-Host 'x'", "한글 😀 $env:X"} {
		if got := decodeCommand(t, encodedCommand(s)); got != s {
			t.Errorf("round trip %q -> %q", s, got)
		}
	}
}

// stubNotify replaces the toast and makes the Start menu shortcut check a
// no-op (stubShortcut overrides it).
func stubNotify(t *testing.T, toast func(string, string) (string, error)) {
	t.Helper()
	st, es := showToast, ensureShortcut
	showToast = toast
	ensureShortcut = func() error { return nil }
	t.Cleanup(func() { showToast, ensureShortcut = st, es })
}

// stubShortcut replaces the Start menu shortcut check; call after
// stubNotify.
func stubShortcut(f func() error) { ensureShortcut = f }

func TestToastAppIDIsTheShortcutAUMID(t *testing.T) {
	if ToastAppID != appid.AUMID {
		t.Errorf("ToastAppID = %q, want the shortcut's AUMID %q", ToastAppID, appid.AUMID)
	}
}

func TestHandleNotifyShortcut(t *testing.T) {
	var order []string
	stubNotify(t,
		func(string, string) (string, error) { order = append(order, "toast"); return "shown", nil })
	stubShortcut(func() error { order = append(order, "shortcut"); return nil })
	out, err := handleNotify(Request{Text: "x"})
	if err != nil || !reflect.DeepEqual(out, map[string]any{"toast": "shown"}) {
		t.Fatalf("shortcut ok: %v, %v", out, err)
	}
	if !reflect.DeepEqual(order, []string{"shortcut", "toast"}) {
		t.Errorf("order %q: the shortcut must exist before the toast", order)
	}

	// A shortcut that cannot be made does not stop the toast.
	shown := false
	stubNotify(t,
		func(string, string) (string, error) { shown = true; return "pending", nil })
	stubShortcut(func() error { return errors.New("start menu shortcut: access denied") })
	out, err = handleNotify(Request{Text: "x"})
	if err != nil || !shown || out["toast"] != "pending" || out["shortcut"] != "failed: start menu shortcut: access denied" {
		t.Errorf("shortcut failed: %v, %v, shown %v", out, err, shown)
	}

	// Both failing: the action fails and says both.
	stubNotify(t, func(string, string) (string, error) { return "", errors.New("toast: no notifier") })
	stubShortcut(func() error { return errors.New("access denied") })
	if _, err := handleNotify(Request{Text: "x"}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("both failed: %v", err)
	}
}

func TestHandleNotify(t *testing.T) {
	var toastArgs [2]string
	stubNotify(t,
		func(title, text string) (string, error) { toastArgs = [2]string{title, text}; return "shown", nil })

	out, err := handleNotify(Request{Action: ActionNotify, Title: "SmartThings", Text: "빨래 끝"})
	if err != nil || !reflect.DeepEqual(out, map[string]any{"toast": "shown"}) {
		t.Fatalf("notify: %v, %v", out, err)
	}
	if toastArgs != [2]string{"SmartThings", "빨래 끝"} {
		t.Errorf("toast got %q", toastArgs)
	}
}

func TestHandleNotifyFailures(t *testing.T) {
	stubNotify(t,
		func(string, string) (string, error) { return "", errors.New("toast: exit status 1: no notifier") })
	if _, err := handleNotify(Request{Text: "x"}); err == nil {
		t.Fatal("a failed toast must fail the action")
	} else if ue := (*Error)(nil); !errors.As(err, &ue) || ue.Code != CodeFailed {
		t.Errorf("err = %v, want failed", err)
	}
}

// The read-aloud feature was dropped (2026-10-01): neither the speak
// action nor notify's --speak/--voice flags are part of the grammar.
func TestSpeakIsNotAnAction(t *testing.T) {
	bad := [][]string{
		{"speak", "--text", "hi"},
		{"notify", "--title", "t", "--text", "hi", "--speak"},
		{"notify", "--title", "t", "--text", "hi", "--voice", "Heami"},
	}
	for _, args := range bad {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) accepted", args)
		}
	}
}
