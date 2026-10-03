package service

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points configDir at a folder of the test run's own, so every
// test that loads or saves config.json, tray.json or state.json does it
// there and never next to the test binary.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stpc-service-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "config folder for the tests:", err)
		os.Exit(1)
	}
	configDir = func() string { return dir }
	// No background session readings (session_fill.go) unless a test asks
	// for them: they would run user-action for real, next to the fakes.
	sys.startFill = func(func()) {}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
