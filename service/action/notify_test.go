package action

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanNotifyText(t *testing.T) {
	for in, want := range map[string]string{
		"빨래 끝":                            "빨래 끝",
		"  a  b  ":                        "a b",
		"line1\nline2\r\nline3\tx":        "line1 line2 line3 x",
		"bell\a esc\x1b[2J null\x00":      "bell esc[2J null",
		"del\x7f c1\u0085":                "del c1", // U+0085 is a space to Go, and a control
		"rtl \u202eevil\u202c mark\u200f": "rtl evil mark",
		"\xff\xfeok":                      "ok",
		"\n\t ":                           "",
		"$(calc) & <b>":                   "$(calc) & <b>", // not ours to escape: the toast XML does
	} {
		if got := CleanNotifyText(in); got != want {
			t.Errorf("CleanNotifyText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrepareNotify(t *testing.T) {
	title, text, err := PrepareNotify("", " 빨래 끝\n")
	if err != nil || title != NotifyDefaultTitle || text != "빨래 끝" {
		t.Errorf("default title: %q %q %v", title, text, err)
	}
	if _, text, err := PrepareNotify("t", strings.Repeat("가", 200)); err != nil || len([]rune(text)) != 200 {
		t.Errorf("200 characters: %v", err)
	}
	// Control characters do not count: 200 visible characters plus noise.
	if _, _, err := PrepareNotify("t", strings.Repeat("가", 200)+"\x00\x01"); err != nil {
		t.Errorf("200 + controls: %v", err)
	}
	for name, c := range map[string][2]string{
		"empty":      {"t", ""},
		"blank":      {"t", " \n\t "},
		"only ctrl":  {"t", "\x00\x1b"},
		"201":        {"t", strings.Repeat("a", 201)},
		"long title": {strings.Repeat("a", 101), "x"},
	} {
		_, _, err := PrepareNotify(c[0], c[1])
		var ne *NotifyError
		if !errors.As(err, &ne) || ne.Code != "bad_text" {
			t.Errorf("%s: err = %v, want bad_text", name, err)
		}
	}
}
