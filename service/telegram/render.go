package telegram

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
)

// Templates live in templates/<lang>/<category>.<kind>.tmpl with a
// generic.tmpl fallback per language. Each file defines up to three blocks:
//
//	{{define "title"}}   ...{{end}}   plain text, one line
//	{{define "summary"}} ...{{end}}   one line (may contain <b>/<code>)
//	{{define "fields"}}  ...{{end}}   optional; one "label: <code>value</code>" per line
//
// The renderer assembles the section-8 layout around them, so templates never
// repeat the icon, blockquote, detail gating or footer. Every Fields value is
// html-escaped before it reaches the template (.Fields), and an esc func is
// available for anything else.
//
//go:embed templates
var templateFS embed.FS

// Detail levels and languages accepted by NewRenderer.
const (
	DetailSimple = "simple"
	DetailFull   = "full"
	LangKo       = "ko"
	LangEn       = "en"
)

const genericTemplate = "generic.tmpl"

// HeaderIcon opens the PC-name header line every Telegram message starts
// with (#75, hub-agent doc §1). Several PCs may share one bot, so the
// name is always shown — it is harmless with a single PC.
const HeaderIcon = "🖥"

// Header is that line for pcName: "🖥 <b>DESKTOP-TEST</b>". The name is
// HTML-escaped; the message body follows on the next line.
func Header(pcName string) string {
	return HeaderIcon + " <b>" + html.EscapeString(pcName) + "</b>"
}

// HasHeader reports whether msg already opens with a PC-name header. The
// icon alone is the marker, so a message read back from Telegram (tags
// stripped) is recognised too.
func HasHeader(msg string) bool { return strings.HasPrefix(msg, HeaderIcon) }

// WithHeader prefixes Header(pcName) to msg unless msg already carries a
// header. Empty stays empty (the poller sends nothing for it).
func WithHeader(pcName, msg string) string {
	if msg == "" || HasHeader(msg) {
		return msg
	}
	return Header(pcName) + "\n" + msg
}

// Renderer turns a notify.Event into Telegram HTML.
type Renderer struct {
	lang   string
	detail string
	tmpls  map[string]*template.Template // "remote.grace_scheduled.tmpl" → parsed set
	// Now is used when Event.At is zero; tests override it.
	Now func() time.Time
}

// templateData is what templates see.
type templateData struct {
	Category string
	Kind     string
	Key      string
	Lang     string
	Full     bool
	PC       string            // escaped
	Time     string            // HH:MM:SS
	Fields   map[string]string // escaped copy of Event.Fields
	Keys     []string          // sorted field names (for generic.tmpl)
}

// NewRenderer parses the embedded templates for lang ("ko"|"en") at the
// given detail level ("simple"|"full").
func NewRenderer(lang string, detail string) (*Renderer, error) {
	switch lang {
	case LangKo, LangEn:
	default:
		return nil, fmt.Errorf("telegram renderer: unsupported lang %q", lang)
	}
	switch detail {
	case DetailSimple, DetailFull:
	default:
		return nil, fmt.Errorf("telegram renderer: unsupported detail %q", detail)
	}
	dir := path.Join("templates", lang)
	entries, err := fs.ReadDir(templateFS, dir)
	if err != nil {
		return nil, fmt.Errorf("telegram renderer: templates for %q: %w", lang, err)
	}
	r := &Renderer{lang: lang, detail: detail, tmpls: map[string]*template.Template{}, Now: time.Now}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmpl") {
			continue
		}
		src, err := templateFS.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("telegram renderer: read %s: %w", e.Name(), err)
		}
		// Tolerate CRLF checkouts (core.autocrlf) so messages never carry \r.
		text := strings.ReplaceAll(string(src), "\r\n", "\n")
		t, err := template.New(e.Name()).Funcs(funcMap(lang)).Option("missingkey=zero").Parse(text)
		if err != nil {
			return nil, fmt.Errorf("telegram renderer: parse %s/%s: %w", lang, e.Name(), err)
		}
		r.tmpls[e.Name()] = t
	}
	if _, ok := r.tmpls[genericTemplate]; !ok {
		return nil, fmt.Errorf("telegram renderer: %s/%s missing", lang, genericTemplate)
	}
	return r, nil
}

// Lang returns the configured language.
func (r *Renderer) Lang() string { return r.lang }

// Detail returns the configured detail level.
func (r *Renderer) Detail() string { return r.detail }

// HasTemplate reports whether a dedicated template exists for the key
// ("category.kind"); false means Render will use generic.tmpl.
func (r *Renderer) HasTemplate(key string) bool {
	_, ok := r.tmpls[key+".tmpl"]
	return ok
}

// funcMap is what templates may call. secs localises a duration given in
// seconds (the aggregation window's window_sec field) as "5분" / "5 min";
// reason localises a power.stopping reason.
func funcMap(lang string) template.FuncMap {
	return template.FuncMap{
		"esc":    html.EscapeString,
		"join":   strings.Join,
		"secs":   func(s string) string { return secondsText(lang, s) },
		"reason": func(s string) string { return stopReasonText(lang, s) },
	}
}

// stopReasonNames is the power.stopping reason wire value (edge-driver doc
// §6.2) in the two languages. "unknown" is a plain service stop, which the
// service cannot tell apart from the start of a shutdown, so it is worded
// as one rather than as "unknown" (#87).
var stopReasonNames = map[string][2]string{
	// {ko, en}
	"shutdown":  {"종료", "Shut down"},
	"restart":   {"재시작", "Restart"},
	"suspend":   {"절전", "Sleep"},
	"hibernate": {"최대 절전", "Hibernate"},
	"unknown":   {"종료", "Shut down"},
}

// stopReasonText localises one power.stopping reason. A reason from a
// newer service is shown as it came, so the message still says something.
func stopReasonText(lang, s string) string {
	names, ok := stopReasonNames[strings.TrimSpace(s)]
	if !ok {
		return s
	}
	if lang == LangEn {
		return names[1]
	}
	return names[0]
}

// secondsText renders a decimal number of seconds as a short localised
// duration: ko "1시간 30분" / "5분" / "90초", en "1 h 30 min" / "5 min" /
// "90 sec". Anything that is not a positive integer is returned unchanged.
func secondsText(lang, s string) string {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return s
	}
	h, m, sec := n/3600, n%3600/60, n%60
	units := [3]string{"시간", "분", "초"}
	if lang == LangEn {
		units = [3]string{" h", " min", " sec"}
	}
	var parts []string
	if h > 0 {
		parts = append(parts, strconv.Itoa(h)+units[0])
	}
	if m > 0 {
		parts = append(parts, strconv.Itoa(m)+units[1])
	}
	if sec > 0 {
		parts = append(parts, strconv.Itoa(sec)+units[2])
	}
	return strings.Join(parts, " ")
}

// Render produces the HTML message for ev:
//
//	🖥 <b>{pcName}</b>                  (#75, always)
//	{icon} <b>{title}</b>
//	<blockquote>{summary}</blockquote>
//	{label}: <code>{value}</code>      (detail=full only, 2–4 lines)
//	<i>{pcName} · {HH:MM:SS}</i>        (detail=full only)
func (r *Renderer) Render(ev notify.Event, pcName string) (string, error) {
	t, ok := r.tmpls[ev.Key()+".tmpl"]
	if !ok {
		t = r.tmpls[genericTemplate]
	}
	at := ev.At
	if at.IsZero() {
		now := r.Now
		if now == nil {
			now = time.Now
		}
		at = now()
	}
	data := templateData{
		Category: ev.Category,
		Kind:     ev.Kind,
		Key:      ev.Key(),
		Lang:     r.lang,
		Full:     r.detail == DetailFull,
		PC:       html.EscapeString(pcName),
		Time:     at.Format("15:04:05"),
		Fields:   make(map[string]string, len(ev.Fields)),
		Keys:     make([]string, 0, len(ev.Fields)),
	}
	for k, v := range ev.Fields {
		data.Fields[k] = html.EscapeString(v)
		data.Keys = append(data.Keys, k)
	}
	sort.Strings(data.Keys)

	title, err := execBlock(t, "title", data)
	if err != nil {
		return "", err
	}
	summary, err := execBlock(t, "summary", data)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(Header(pcName))
	b.WriteString("\n")
	b.WriteString(IconFor(ev.Category, ev.Kind))
	b.WriteString(" <b>")
	b.WriteString(oneLine(title))
	b.WriteString("</b>\n<blockquote>")
	b.WriteString(strings.TrimSpace(summary))
	b.WriteString("</blockquote>")
	if !data.Full {
		return b.String(), nil
	}
	if t.Lookup("fields") != nil {
		fields, err := execBlock(t, "fields", data)
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(fields, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			b.WriteString("\n")
			b.WriteString(line)
		}
	}
	b.WriteString("\n<i>")
	b.WriteString(data.PC)
	b.WriteString(" · ")
	b.WriteString(data.Time)
	b.WriteString("</i>")
	return b.String(), nil
}

func execBlock(t *template.Template, name string, data templateData) (string, error) {
	if t.Lookup(name) == nil {
		return "", fmt.Errorf("telegram renderer: %s lacks {{define %q}}", t.Name(), name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("telegram renderer: %s/%s: %w", t.Name(), name, err)
	}
	return buf.String(), nil
}

// oneLine collapses whitespace runs (including newlines) into single spaces.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// IconFor returns the section-8 icon for an event.
func IconFor(category, kind string) string {
	switch category {
	case "remote":
		return "🔌"
	case "schedule":
		return "⏱"
	case "power":
		switch kind {
		case "started":
			return "🟢"
		case "resumed":
			return "🌙"
		case "stopping":
			return "🔴"
		}
		return "⚡"
	case "security":
		return "🛡"
	case "system":
		switch kind {
		case "update_available", "updated":
			return "🆕"
		case "digest":
			return "📋"
		case "test":
			return "✅"
		}
		return "⚠️"
	}
	return "🔔"
}
