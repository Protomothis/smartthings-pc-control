package secret

import "testing"

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"abc":      "****",
		"1234567":  "****",
		"12345678": "****5678",
		"123456789:ABCdefGHIjklMNOpqrSTUvwxYZ0123456789": "****6789",
		"한글토큰값입니다1234":                                   "****1234",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsProtected(t *testing.T) {
	for _, s := range []string{"", "plain-token", "dpapi:", "DPAPI:abc"} {
		if IsProtected(s) {
			t.Errorf("IsProtected(%q) = true, want false", s)
		}
	}
	if !IsProtected("dpapi:AQAAANCMnd8BFdERjHoAwE") {
		t.Error("prefixed value must be reported as protected")
	}
}
