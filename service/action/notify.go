package action

// The text rules of a PC notification (#106, docs/design/media-notify.md
// §3): SmartThings, Telegram /say and the app's test button all go through
// PrepareNotify.

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Protomothis/smartthings-pc-control/useraction"
)

const (
	// NotifyDefaultTitle is the toast title when a request sends none.
	NotifyDefaultTitle = "SmartThings"
	// NotifyMaxText and NotifyMaxTitle are counted in characters (runes)
	// after control characters are removed (§3).
	NotifyMaxText  = useraction.MaxTextRunes
	NotifyMaxTitle = useraction.MaxTitleRunes
)

// CleanNotifyText removes what must not reach a toast: line breaks and
// tabs become spaces, every other control character and the bidirectional
// overrides (which could make a toast read differently from what was sent)
// are dropped, and runs of spaces collapse. Surrounding space is trimmed.
func CleanNotifyText(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r): // \n, \r, \t included
			space = true
			continue
		case unicode.IsControl(r), isBidiControl(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// isBidiControl reports the explicit directional formatting characters
// (LRE…RLO, LRI…PDI) and the marks ALM, LRM, RLM.
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) ||
		r == 0x061C || r == 0x200E || r == 0x200F
}

// PrepareNotify validates and cleans one request's title and text.
func PrepareNotify(title, text string) (string, string, error) {
	text = CleanNotifyText(text)
	if n := utf8.RuneCountInString(text); n == 0 || n > NotifyMaxText {
		return "", "", &NotifyError{Code: "bad_text",
			Message: fmt.Sprintf("text must be 1-%d characters (got %d)", NotifyMaxText, n)}
	}
	title = CleanNotifyText(title)
	if title == "" {
		title = NotifyDefaultTitle
	}
	if n := utf8.RuneCountInString(title); n > NotifyMaxTitle {
		return "", "", &NotifyError{Code: "bad_text",
			Message: fmt.Sprintf("title must be at most %d characters (got %d)", NotifyMaxTitle, n)}
	}
	return title, text, nil
}
