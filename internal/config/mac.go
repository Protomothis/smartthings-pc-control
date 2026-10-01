package config

import "strings"

// NormalizeMAC converts any spelling of a 6-byte MAC — "B4-2E-99-45-B4-F5",
// "b4:2e:99:45:b4:f5", "b4.2e.99.45.b4.f5", "b42e9945b4f5" — to the
// upper-case dash form Windows and the adapter list use. Anything that is
// not exactly 12 hex digits (with optional separators) returns "", which
// every caller reads as "no MAC".
func NormalizeMAC(s string) string {
	digits := make([]byte, 0, 12)
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'F':
			digits = append(digits, byte(r))
		case r >= 'a' && r <= 'f':
			digits = append(digits, byte(r-'a'+'A'))
		case r == '-', r == ':', r == '.', r == ' ':
			// separator, ignored
		default:
			return ""
		}
		if len(digits) > 12 {
			return ""
		}
	}
	if len(digits) != 12 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.Write(digits[i : i+2])
	}
	return b.String()
}
