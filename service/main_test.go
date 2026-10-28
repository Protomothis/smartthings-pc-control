package service

import (
	"fmt"
	"os"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/service/power"
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
	// Device slots filled by a test's device lists (#159) stay in memory:
	// a config.json written behind a test's back would reach the next one
	// that loads it. audio_devices_test.go checks the real save.
	saveSlots = func(cfg Config) error {
		setConfig(cfg.WithDefaults())
		return nil
	}
	// A desktop with S3 whatever the test machine is: on a modern standby
	// laptop the suspend and screen commands take another path (#162),
	// which the tests that want it fake on their own (fakePowerCaps).
	sys.powerCaps = func() (power.Capabilities, error) { return power.Capabilities{S3: true}, nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
