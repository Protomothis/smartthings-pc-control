package telegram

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/")

// catalogue mirrors section 3 of the design doc plus system.test and
// system.digest, with sample field values. Field values are deliberately the
// same for ko and en so the goldens differ only in template text.
var catalogue = []notify.Event{
	{Category: "remote", Kind: "received", Fields: map[string]string{"command": "lock", "from": "192.168.1.20"}},
	{Category: "remote", Kind: "grace_scheduled", Fields: map[string]string{"command": "shutdown", "from": "192.168.1.20", "delay": "5분", "execute_at": "14:40:00"},
		Actions: []notify.Action{{Label: "바로 실행", Data: "runnow:"}, {Label: "취소", Data: "cancel:"}}},
	{Category: "remote", Kind: "grace_cancelled", Fields: map[string]string{"command": "shutdown", "by": "toast"}},
	{Category: "remote", Kind: "executed", Fields: map[string]string{"command": "shutdown"}},
	{Category: "remote", Kind: "force", Fields: map[string]string{"from": "192.168.1.20"}},

	{Category: "schedule", Kind: "created", Fields: map[string]string{"command": "sleep", "origin": "app", "delay": "30분", "execute_at": "15:05:00"}},
	{Category: "schedule", Kind: "cancelled", Fields: map[string]string{"command": "sleep", "origin": "app", "by": "webui"}},
	{Category: "schedule", Kind: "executed", Fields: map[string]string{"command": "sleep", "origin": "app"}},
	{Category: "schedule", Kind: "replaced", Fields: map[string]string{"command": "shutdown", "origin": "telegram", "old_command": "sleep", "old_origin": "app"}},

	{Category: "power", Kind: "started", Fields: map[string]string{"version": "v1.0.0", "boot_time": "2026-09-10 14:34:12", "external_ip": "203.0.113.7"}},
	{Category: "power", Kind: "resumed", Fields: map[string]string{"since": "2시간 10분"}},
	{Category: "power", Kind: "stopping", Fields: map[string]string{"reason": "shutdown"}},

	{Category: "security", Kind: "unauthorized", Fields: map[string]string{"from": "10.0.0.5, 10.0.0.9 외 1곳", "path": "/shutdown", "count": "12", "window": "5분"}},
	{Category: "security", Kind: "login_limited", Fields: map[string]string{"from": "10.0.0.5"}},
	{Category: "security", Kind: "unknown_command", Fields: map[string]string{"from": "10.0.0.5", "command": "explode"}},
	{Category: "security", Kind: "config_changed", Fields: map[string]string{"keys": "secret, port", "by": "webui"}},
	{Category: "security", Kind: "unknown_chat", Fields: map[string]string{"chat_id": "987654321", "username": "stranger", "text": "/shutdown"}},

	{Category: "system", Kind: "update_available", Fields: map[string]string{"version": "v1.1.0", "current": "v1.0.0", "url": "https://github.com/Protomothis/smartthings-pc-control/releases/tag/v1.1.0"}},
	{Category: "system", Kind: "updated", Fields: map[string]string{"version": "v1.0.0", "previous": "v0.3.4"}},
	{Category: "system", Kind: "exec_failed", Fields: map[string]string{"command": "hibernate", "error": "exit status 1"}},
	{Category: "system", Kind: "tray_wake_failed", Fields: map[string]string{"command": "screenoff", "error": "no active session"}},
	{Category: "system", Kind: "test", Fields: map[string]string{}},
	{Category: "system", Kind: "digest", Fields: map[string]string{"count": "3", "period": "22:00–07:00", "items": "🔌 remote.received lock\n⏱ schedule.executed sleep\n🛡 security.unauthorized 10.0.0.5"}},
}

var goldenTime = time.Date(2026, 9, 10, 14, 35, 0, 0, time.Local)

const goldenPC = "DESKTOP-TEST"

func TestRenderGolden(t *testing.T) {
	for _, lang := range []string{LangKo, LangEn} {
		for _, detail := range []string{DetailSimple, DetailFull} {
			t.Run(lang+"_"+detail, func(t *testing.T) {
				r, err := NewRenderer(lang, detail)
				if err != nil {
					t.Fatal(err)
				}
				var b strings.Builder
				for _, ev := range catalogue {
					ev.At = goldenTime
					if !r.HasTemplate(ev.Key()) {
						t.Errorf("%s: no %s template, would fall back to generic", ev.Key(), lang)
					}
					out, err := r.Render(ev, goldenPC)
					if err != nil {
						t.Fatalf("%s: %v", ev.Key(), err)
					}
					b.WriteString("=== " + ev.Key() + " ===\n")
					b.WriteString(out)
					b.WriteString("\n\n")
				}
				got := b.String()
				path := filepath.Join("testdata", "render_"+lang+"_"+detail+".golden")
				if *update {
					if err := os.MkdirAll("testdata", 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden: %v (run: go test ./service/telegram -update)", err)
				}
				if norm(string(want)) != norm(got) {
					t.Errorf("rendered output differs from %s (run with -update to regenerate)\n--- got ---\n%s", path, got)
				}
			})
		}
	}
}

// norm makes the comparison tolerant of CRLF checkouts.
func norm(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func TestRenderLayoutSimpleVsFull(t *testing.T) {
	ev := testEvent()
	simple, _ := NewRenderer("ko", "simple")
	full, _ := NewRenderer("ko", "full")
	s, err := simple.Render(ev, goldenPC)
	if err != nil {
		t.Fatal(err)
	}
	f, err := full.Render(ev, goldenPC)
	if err != nil {
		t.Fatal(err)
	}
	sl := strings.Split(s, "\n")
	if len(sl) != 2 {
		t.Errorf("simple should be exactly two lines, got %d: %q", len(sl), s)
	}
	if !strings.HasPrefix(sl[0], "🔌 <b>") || !strings.HasSuffix(sl[0], "</b>") {
		t.Errorf("title line = %q", sl[0])
	}
	if !strings.HasPrefix(sl[1], "<blockquote>") || !strings.HasSuffix(sl[1], "</blockquote>") {
		t.Errorf("summary line = %q", sl[1])
	}
	if !strings.HasPrefix(f, s) {
		t.Errorf("full should start with the simple rendering\nsimple: %q\nfull: %q", s, f)
	}
	fl := strings.Split(f, "\n")
	if got := len(fl) - 3; got < 2 || got > 4 {
		t.Errorf("full should have 2–4 field lines, got %d: %q", got, f)
	}
	for _, line := range fl[2 : len(fl)-1] {
		if !strings.Contains(line, ": <code>") || !strings.HasSuffix(line, "</code>") {
			t.Errorf("field line = %q", line)
		}
	}
	if fl[len(fl)-1] != "<i>DESKTOP-TEST · 14:35:00</i>" {
		t.Errorf("footer = %q", fl[len(fl)-1])
	}
}

func TestRenderEscapesFieldValues(t *testing.T) {
	for _, lang := range []string{"ko", "en"} {
		r, _ := NewRenderer(lang, "full")
		ev := notify.Event{Category: "remote", Kind: "received", At: goldenTime,
			Fields: map[string]string{"command": "<b>&", "from": "a<u>b"}}
		out, err := r.Render(ev, "PC <1>")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "<code>&lt;b&gt;&amp;</code>") {
			t.Errorf("%s: field not escaped: %q", lang, out)
		}
		// Templates never emit <u>, so any occurrence is a leaked raw value.
		if strings.Contains(out, "<u>") || strings.Contains(out, "&\n") {
			t.Errorf("%s: raw markup leaked: %q", lang, out)
		}
		// Exactly the template's own <b>…</b> pair survives; the value's does not.
		if strings.Count(out, "<b>") != 2 || strings.Count(out, "</b>") != 2 {
			t.Errorf("%s: unexpected <b> count: %q", lang, out)
		}
		if !strings.Contains(out, "<i>PC &lt;1&gt; · ") {
			t.Errorf("%s: pc name not escaped: %q", lang, out)
		}
	}
}

func TestRenderUnknownKindFallsBackToGeneric(t *testing.T) {
	r, _ := NewRenderer("en", "full")
	ev := notify.Event{Category: "remote", Kind: "does_not_exist", At: goldenTime,
		Fields: map[string]string{"zeta": "1", "alpha": "x&y"}}
	if r.HasTemplate(ev.Key()) {
		t.Fatal("test needs an unknown kind")
	}
	out, err := r.Render(ev, goldenPC)
	if err != nil {
		t.Fatal(err)
	}
	want := "🔌 <b>remote.does_not_exist</b>\n<blockquote>alpha: x&amp;y · zeta: 1</blockquote>\nalpha: <code>x&amp;y</code>\nzeta: <code>1</code>\n<i>DESKTOP-TEST · 14:35:00</i>"
	if out != want {
		t.Errorf("generic rendering\n got: %q\nwant: %q", out, want)
	}
	// Unknown category gets the bell icon.
	ev.Category = "nope"
	out, _ = r.Render(ev, goldenPC)
	if !strings.HasPrefix(out, "🔔 <b>nope.does_not_exist</b>") {
		t.Errorf("unknown category: %q", out)
	}
}

func TestRenderMissingFieldIsEmptyNotNoValue(t *testing.T) {
	r, _ := NewRenderer("ko", "full")
	ev := notify.Event{Category: "remote", Kind: "received", At: goldenTime, Fields: nil}
	out, err := r.Render(ev, goldenPC)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "<no value>") {
		t.Errorf("missing field rendered as <no value>: %q", out)
	}
}

func TestRenderZeroTimeUsesNow(t *testing.T) {
	r, _ := NewRenderer("en", "full")
	r.Now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local) }
	out, err := r.Render(notify.Event{Category: "system", Kind: "test"}, goldenPC)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "<i>DESKTOP-TEST · 03:04:05</i>") {
		t.Errorf("footer time: %q", out)
	}
}

func TestNewRendererRejectsBadArgs(t *testing.T) {
	if _, err := NewRenderer("fr", "full"); err == nil {
		t.Error("lang fr should be rejected")
	}
	if _, err := NewRenderer("ko", "verbose"); err == nil {
		t.Error("detail verbose should be rejected")
	}
	r, err := NewRenderer("en", "simple")
	if err != nil || r.Lang() != "en" || r.Detail() != "simple" {
		t.Errorf("NewRenderer(en, simple) = %v, %v", r, err)
	}
}

func TestTemplateSetsMatchAcrossLanguages(t *testing.T) {
	list := func(lang string) map[string]bool {
		entries, err := fs.ReadDir(templateFS, "templates/"+lang)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, e := range entries {
			m[e.Name()] = true
		}
		return m
	}
	ko, en := list("ko"), list("en")
	for name := range ko {
		if !en[name] {
			t.Errorf("templates/en/%s missing", name)
		}
	}
	for name := range en {
		if !ko[name] {
			t.Errorf("templates/ko/%s missing", name)
		}
	}
	// Every catalogue event has a dedicated template; no orphan templates.
	known := map[string]bool{genericTemplate: true}
	for _, ev := range catalogue {
		known[ev.Key()+".tmpl"] = true
		if !ko[ev.Key()+".tmpl"] {
			t.Errorf("templates/ko/%s.tmpl missing", ev.Key())
		}
	}
	for name := range ko {
		if !known[name] {
			t.Errorf("templates/ko/%s is not in the catalogue", name)
		}
	}
}

func TestIconFor(t *testing.T) {
	cases := map[[2]string]string{
		{"remote", "received"}:         "🔌",
		{"schedule", "created"}:        "⏱",
		{"power", "started"}:           "🟢",
		{"power", "resumed"}:           "🌙",
		{"power", "stopping"}:          "🔴",
		{"security", "unauthorized"}:   "🛡",
		{"system", "update_available"}: "🆕",
		{"system", "updated"}:          "🆕",
		{"system", "exec_failed"}:      "⚠️",
		{"system", "tray_wake_failed"}: "⚠️",
		{"system", "digest"}:           "📋",
		{"system", "test"}:             "✅",
		{"whatever", "x"}:              "🔔",
	}
	for k, want := range cases {
		if got := IconFor(k[0], k[1]); got != want {
			t.Errorf("IconFor(%s, %s) = %q, want %q", k[0], k[1], got, want)
		}
	}
}
