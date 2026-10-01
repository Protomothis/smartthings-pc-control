package secret

import "testing"

func TestSecretEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"mysecret", "mysecret", true},
		{"", "", true},
		{"mysecret", "mysecreT", false},
		{"mysecret", "mysecret ", false},
		{"mysecret", "", false},
		{"", "mysecret", false},
		{"short", "a much longer secret", false},
		{"비밀", "비밀", true},
	}
	for _, c := range cases {
		if got := Equal(c.a, c.b); got != c.want {
			t.Errorf("Equal(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
