//go:build windows

package secret

import (
	"strings"
	"testing"
)

func TestProtectUnprotectRoundTrip(t *testing.T) {
	const token = "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ0123456789"
	stored, err := Protect(token)
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}
	if !IsProtected(stored) {
		t.Fatal("Protect output lacks prefix")
	}
	if strings.Contains(stored, token) || stored == token {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := Unprotect(stored)
	if err != nil {
		t.Fatalf("Unprotect: %v", err)
	}
	if got != token {
		t.Fatal("round trip mismatch")
	}
}

func TestProtectTwiceBothDecrypt(t *testing.T) {
	const v = "same-value-twice"
	for i := 0; i < 2; i++ {
		s, err := Protect(v)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Unprotect(s)
		if err != nil || got != v {
			t.Fatalf("attempt %d: Unprotect mismatch (err=%v)", i, err)
		}
	}
}

func TestUnprotectPassesPlaintextThrough(t *testing.T) {
	got, err := Unprotect("legacy-plain-token")
	if err != nil {
		t.Fatal(err)
	}
	if got != "legacy-plain-token" {
		t.Fatalf("got %q", got)
	}
}

func TestEmpty(t *testing.T) {
	if s, err := Protect(""); s != "" || err != nil {
		t.Fatalf("Protect(\"\") = %q, %v", s, err)
	}
	if s, err := Unprotect(""); s != "" || err != nil {
		t.Fatalf("Unprotect(\"\") = %q, %v", s, err)
	}
	if _, err := Unprotect("dpapi:"); err != nil {
		// "dpapi:" alone has no payload; IsProtected is false so it passes through.
		t.Fatalf("Unprotect(\"dpapi:\") should pass through, got %v", err)
	}
}

func TestUnprotectRejectsBadPayload(t *testing.T) {
	stored, err := Protect("tamper-me-please")
	if err != nil {
		t.Fatal(err)
	}
	// Flip a character near the end of the base64 body (inside the encrypted
	// data / MAC, past the informational header): still valid base64, but a
	// corrupted ciphertext.
	b := []byte(stored)
	i := len(b) - 8
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	if got, err := Unprotect(string(b)); err == nil {
		t.Errorf("tampered ciphertext should fail, got %d bytes", len(got))
	}
	if _, err := Unprotect("dpapi:not*base64!"); err == nil {
		t.Error("invalid base64 should fail")
	}
	if _, err := Unprotect("dpapi:AQID"); err == nil {
		t.Error("garbage ciphertext should fail")
	}
	if _, err := Unprotect("dpapi:" + strings.Repeat("A", 4)); err == nil {
		t.Error("zero-byte ciphertext should fail")
	}
}
