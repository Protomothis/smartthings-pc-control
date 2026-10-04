package logx

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "(none)"},
		{"ab", "***"},
		{"abcd", "***"},
		{"abcde", "ab***de"},
		{"mysecretkey", "my***ey"},
	}

	for _, tt := range tests {
		result := MaskSecret(tt.input)
		if result != tt.expected {
			t.Errorf("MaskSecret(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

// ErrorLog lands in the log as one line per message (#133).
func TestErrorLogWritesThroughPrintf(t *testing.T) {
	var buf bytes.Buffer
	restore := Capture(&buf)
	defer restore()
	ErrorLog().Printf("http: panic serving 127.0.0.1:1: boom\ngoroutine 7 [running]:\n")
	if got := buf.String(); got != "http: panic serving 127.0.0.1:1: boom\ngoroutine 7 [running]:\n" {
		t.Errorf("logged %q", got)
	}
}

func TestInitWritesAndCaptureRedirects(t *testing.T) {
	Close()
	t.Cleanup(Close)
	Printf("dropped before Init")
	dir := t.TempDir()
	Init(dir)
	Printf("hello %d", 1)

	var buf bytes.Buffer
	restore := Capture(&buf)
	Printf("captured")
	restore()
	Printf("after")
	Close()

	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "hello 1") || !strings.Contains(got, "after") || strings.Contains(got, "captured") || strings.Contains(got, "dropped") {
		t.Errorf("log file:\n%s", got)
	}
	if buf.String() != "captured\n" {
		t.Errorf("captured %q", buf.String())
	}
}
